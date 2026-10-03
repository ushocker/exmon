package wlan

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// These structs copy the memory layout of the Win32 structs named in each comment.
// Field order, sizes and array lengths must match the C definitions exactly.
// layout_test.go checks the sizes and offsets.

type dot11SSID struct { // DOT11_SSID (36 bytes)
	Length uint32
	SSID   [32]byte
}

type interfaceInfo struct { // WLAN_INTERFACE_INFO (532 bytes)
	InterfaceGUID windows.GUID
	Description   [256]uint16
	State         uint32
}

type interfaceInfoList struct { // WLAN_INTERFACE_INFO_LIST
	NumberOfItems uint32
	Index         uint32
	InterfaceInfo [1]interfaceInfo // really NumberOfItems long
}

type associationAttributes struct { // WLAN_ASSOCIATION_ATTRIBUTES
	SSID          dot11SSID
	BssType       uint32
	BSSID         [6]byte
	PhyType       uint32
	PhyIndex      uint32
	SignalQuality uint32 // 0-100
	RxRate        uint32 // kbps
	TxRate        uint32 // kbps
}

type securityAttributes struct { // WLAN_SECURITY_ATTRIBUTES
	SecurityEnabled int32
	OneXEnabled     int32
	AuthAlgorithm   uint32
	CipherAlgorithm uint32
}

type connectionAttributes struct { // WLAN_CONNECTION_ATTRIBUTES
	State          uint32
	ConnectionMode uint32
	ProfileName    [256]uint16
	Association    associationAttributes
	Security       securityAttributes
}

type rateSet struct { // WLAN_RATE_SET
	Length uint32
	Rates  [126]uint16
}

type bssEntry struct { // WLAN_BSS_ENTRY (360 bytes)
	SSID              dot11SSID
	PhyID             uint32
	BSSID             [6]byte
	BssType           uint32
	PhyType           uint32
	RSSI              int32 // dBm
	LinkQuality       uint32
	InRegDomain       uint8
	BeaconPeriod      uint16
	Timestamp         uint64
	HostTimestamp     uint64
	CapabilityInfo    uint16
	ChCenterFrequency uint32 // kHz
	RateSet           rateSet
	IEOffset          uint32 // from the start of this entry
	IESize            uint32
}

type bssList struct { // WLAN_BSS_LIST
	TotalSize     uint32
	NumberOfItems uint32
	Entries       [1]bssEntry // really NumberOfItems long
}

type macFrameStatistics struct { // WLAN_MAC_FRAME_STATISTICS
	TransmittedFrameCount uint64
	ReceivedFrameCount    uint64
	WEPExcludedCount      uint64
	TKIPLocalMICFailures  uint64
	TKIPReplays           uint64
	TKIPICVErrorCount     uint64
	CCMPReplays           uint64
	CCMPDecryptErrors     uint64
	WEPUndecryptableCount uint64
	WEPICVErrorCount      uint64
	DecryptSuccessCount   uint64
	DecryptFailureCount   uint64
}

type phyFrameStatistics struct { // WLAN_PHY_FRAME_STATISTICS (144 bytes)
	TransmittedFrameCount            uint64
	MulticastTransmittedFrameCount   uint64
	FailedCount                      uint64
	RetryCount                       uint64
	MultipleRetryCount               uint64
	MaxTXLifetimeExceededCount       uint64
	TransmittedFragmentCount         uint64
	RTSSuccessCount                  uint64
	RTSFailureCount                  uint64
	ACKFailureCount                  uint64
	ReceivedFrameCount               uint64
	MulticastReceivedFrameCount      uint64
	PromiscuousReceivedFrameCount    uint64
	MaxRXLifetimeExceededCount       uint64
	FrameDuplicateCount              uint64
	ReceivedFragmentCount            uint64
	PromiscuousReceivedFragmentCount uint64
	FCSErrorCount                    uint64
}

type statistics struct { // WLAN_STATISTICS
	FourWayHandshakeFailures   uint64
	TKIPCounterMeasuresInvoked uint64
	Reserved                   uint64
	MacUcastCounters           macFrameStatistics
	MacMcastCounters           macFrameStatistics
	NumberOfPhys               uint32
	PhyCounters                [1]phyFrameStatistics // really NumberOfPhys long
}

type notificationData struct { // WLAN_NOTIFICATION_DATA
	Source        uint32
	Code          uint32
	InterfaceGUID windows.GUID
	DataSize      uint32
	Data          unsafe.Pointer
}

type connectionNotificationData struct { // WLAN_CONNECTION_NOTIFICATION_DATA (fixed part, without flags and profile XML)
	ConnectionMode  uint32
	ProfileName     [256]uint16
	SSID            dot11SSID
	BssType         uint32
	SecurityEnabled int32
	ReasonCode      uint32
}

type msmNotificationData struct { // WLAN_MSM_NOTIFICATION_DATA
	ConnectionMode  uint32
	ProfileName     [256]uint16
	SSID            dot11SSID
	BssType         uint32
	MacAddr         [6]byte
	SecurityEnabled int32
	FirstPeer       int32
	LastPeer        int32
	ReasonCode      uint32
}

type phyRadioState struct { // WLAN_PHY_RADIO_STATE
	PhyIndex      uint32
	SoftwareState uint32 // DOT11_RADIO_STATE: 1 on, 2 off
	HardwareState uint32
}
