package wakeonlan

import "net"

func BuildMagicPacket(mac net.HardwareAddr) []byte {
	macBytes := []byte(mac)
	// Pre-allocate packet memory
	packetLen := 6 + 16*len(macBytes)
	mp := make([]byte, packetLen)
	for idx := 0; idx < 6; idx++ {
		mp[idx] = 0xFF
	}
	for idx := 6; idx < packetLen; idx += len(macBytes) {
		// Does not check slice capacity
		copy(mp[idx:], macBytes)
	}
	return mp
}
