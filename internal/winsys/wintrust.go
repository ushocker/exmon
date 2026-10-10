package winsys

// Authenticode: checking that an .exe carries a valid code signature, and who
// signed it. The updater uses this before swapping in a new exmon.exe.

import (
	"crypto/x509"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCryptMsgGetParam = crypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose    = crypt32.NewProc("CryptMsgClose")
)

const cmsgSignerCertInfoParam = 7 // CMSG_SIGNER_CERT_INFO_PARAM: issuer + serial of the first signer

// VerifyAuthenticode checks the file's embedded code signature the way Windows
// does when you run a downloaded program (WinVerifyTrust: the signature matches
// the file, the certificate chains to a trusted root, and is valid for code
// signing), then returns the signing certificate so the caller can check it is
// from the expected publisher. Revocation is checked for the whole chain.
func VerifyAuthenticode(path string) (*x509.Certificate, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	file := windows.WinTrustFileInfo{FilePath: p}
	file.Size = uint32(unsafe.Sizeof(file))
	data := windows.WinTrustData{
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_WHOLECHAIN,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&file),
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
	}
	data.Size = uint32(unsafe.Sizeof(data))
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	// Always release the state WinVerifyTrust kept, whatever the result.
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	if verifyErr != nil {
		return nil, fmt.Errorf("code signature: %w", verifyErr)
	}
	return signerCertificate(path)
}

// signerCertificate reads the first signer's certificate from a signed file's
// embedded PKCS#7 signature.
func signerCertificate(path string) (*x509.Certificate, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var encoding, contentType, formatType uint32
	var store, msg windows.Handle
	err = windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(p),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0,
		&encoding, &contentType, &formatType, &store, &msg, nil)
	if err != nil {
		return nil, fmt.Errorf("read the signature: %w", err)
	}
	defer windows.CertCloseStore(store, 0)
	defer cryptMsgClose(msg)

	info, err := cryptMsgGetParam(msg, cmsgSignerCertInfoParam)
	if err != nil {
		return nil, err
	}
	// info holds a CERT_INFO with only the issuer and serial number filled in,
	// which is what CERT_FIND_SUBJECT_CERT looks for.
	ctx, err := windows.CertFindCertificateInStore(store, encoding, 0, windows.CERT_FIND_SUBJECT_CERT,
		unsafe.Pointer(&info[0]), nil)
	if err != nil {
		return nil, fmt.Errorf("find the signing certificate: %w", err)
	}
	defer windows.CertFreeCertificateContext(ctx)
	return x509.ParseCertificate(certBytes(ctx))
}

// cryptMsgGetParam wraps CryptMsgGetParam (size first, then the data).
func cryptMsgGetParam(msg windows.Handle, param uint32) ([]byte, error) {
	var size uint32
	r, _, err := procCryptMsgGetParam.Call(uintptr(msg), uintptr(param), 0, 0, uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return nil, fmt.Errorf("CryptMsgGetParam: %w", err)
	}
	if size == 0 {
		return nil, errors.New("CryptMsgGetParam: empty")
	}
	// A []uint64 keeps the buffer 8-byte aligned, as the CERT_INFO inside needs.
	words := make([]uint64, (size+7)/8)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), size)
	r, _, err = procCryptMsgGetParam.Call(uintptr(msg), uintptr(param), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return nil, fmt.Errorf("CryptMsgGetParam: %w", err)
	}
	return buf, nil
}

func cryptMsgClose(msg windows.Handle) {
	procCryptMsgClose.Call(uintptr(msg))
}
