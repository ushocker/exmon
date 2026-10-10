package winsys

import (
	"crypto/x509"
	"fmt"

	"golang.org/x/sys/windows"
)

// ClientCertMatch selects a client certificate: an exact subject CN and/or an
// issuer CN ("" = any), and the Client Authentication purpose (or no stated
// purpose). exmon's control client and exmon-helper use the same rule, so they
// always agree on which certificate is the device's.
func ClientCertMatch(subjectCN, issuerCN string) func(*x509.Certificate) bool {
	return func(c *x509.Certificate) bool {
		if subjectCN != "" && c.Subject.CommonName != subjectCN {
			return false
		}
		if issuerCN != "" && c.Issuer.CommonName != issuerCN {
			return false
		}
		if len(c.ExtKeyUsage) == 0 {
			return true
		}
		for _, u := range c.ExtKeyUsage {
			if u == x509.ExtKeyUsageClientAuth {
				return true
			}
		}
		return false
	}
}

// SetServiceDACL replaces who may do what with a service (start, stop, query,
// reconfigure ...). h must be a service handle opened with WRITE_DAC.
func SetServiceDACL(h windows.Handle, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse service permissions %q: %w", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// StartServiceOnly starts a service asking for nothing but the right to start
// it, which is all exmon (LocalService) is granted on exmon-helper. It returns nil
// if the service is already running.
func StartServiceOnly(name string) error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("open the service manager: %w", err)
	}
	defer windows.CloseServiceHandle(scm)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	s, err := windows.OpenService(scm, n, windows.SERVICE_START)
	if err != nil {
		return fmt.Errorf("open service %s: %w", name, err)
	}
	defer windows.CloseServiceHandle(s)
	if err := windows.StartService(s, 0, nil); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		return fmt.Errorf("start %s: %w", name, err)
	}
	return nil
}
