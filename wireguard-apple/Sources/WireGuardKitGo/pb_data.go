package main

func encodeDataPacket(payload []byte) []byte {
	userPacket := make([]byte, 0, 1+pbVarintSize(uint64(len(payload)))+len(payload))
	userPacket = pbAppendBytes(userPacket, 2, payload)

	packet := make([]byte, 0, 1+pbVarintSize(uint64(len(userPacket)))+len(userPacket))
	packet = pbAppendBytes(packet, 2, userPacket)
	return packet
}

func decodeDataPacket(packet []byte) ([]byte, bool) {
	userPackets := pbAll(packet, 2)
	if len(userPackets) == 0 {
		return nil, false
	}

	payloads := pbAll(userPackets[0], 2)
	if len(payloads) == 0 || len(payloads[0]) == 0 {
		return nil, false
	}

	payload := make([]byte, len(payloads[0]))
	copy(payload, payloads[0])
	return payload, true
}

func pbAppendBytes(dst []byte, field uint64, value []byte) []byte {
	dst = pbAppendVarint(dst, field<<3|2)
	dst = pbAppendVarint(dst, uint64(len(value)))
	dst = append(dst, value...)
	return dst
}

func pbAppendVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func pbVarintSize(value uint64) int {
	size := 1
	for value >= 0x80 {
		size++
		value >>= 7
	}
	return size
}
