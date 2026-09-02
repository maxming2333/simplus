//go:build !linux

package standardsms

func openSerialSession(string) (serialSession, error) {
	return nil, ErrTTYUnsupported
}
