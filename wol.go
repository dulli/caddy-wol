package wakeonlan

import "net"

func BuildMagicPacket(mac net.HardwareAddr) []byte {
	macBytes := []byte(mac)
	mp := make([]byte, 6)
	for idx := 0; idx < 6; idx++ {
		mp[idx] = 0xFF
	}
	for idx := 0; idx < 16; idx++ {
		// TODO: refactor to pre-allocate the packet memory
		mp = append(mp, macBytes...)
	}
	return mp
}
