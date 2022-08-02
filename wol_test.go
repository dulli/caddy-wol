package wakeonlan

import (
	"bytes"
	"net"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	mac, _ := net.ParseMAC("CC:C4:45:32:7A:51")
	macBytes := []byte(mac)
	header := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	mp := BuildMagicPacket(mac)

	offset := len(header)
	t.Run("Header", func(t *testing.T) {
		data := mp[:offset]
		if !bytes.Equal(data, header) {
			t.Errorf("incorrect packet header: %x", data)
		}
	})

	t.Run("Payload", func(t *testing.T) {
		for idx := 0; idx < 16; idx++ {
			data := mp[offset : offset+len(macBytes)]
			offset = offset + len(macBytes)
			if !bytes.Equal(data, macBytes) {
				t.Errorf("incorrect packet payload at position %d: %x", idx, data)
			}
		}
	})
}
