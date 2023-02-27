package bufferproxy

import "io"

func Gen(byteCount int64, outputWriter io.Writer) error {
	diff := int('~') - int('!')
	buf := make([]byte, 300*diff)
	for i := range buf {
		buf[i] = byte('!' + i%diff)
	}

	for byteCount > 0 {
		var count int64
		if byteCount > int64(len(buf)) {
			count = int64(len(buf))
		} else {
			count = byteCount
		}

		_, err := outputWriter.Write(buf[:count])
		if err != nil {
			return err
		}

		byteCount -= count
	}
	return nil
}
