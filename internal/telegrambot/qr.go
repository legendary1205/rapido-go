package telegrambot

import qrcode "github.com/skip2/go-qrcode"

// qrPNG renders content as a QR code large enough to scan off a phone screen.
func qrPNG(content string) ([]byte, error) {
	return qrcode.Encode(content, qrcode.Medium, 640)
}
