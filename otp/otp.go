package otp

import (
	"bytes"
	"fmt"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// func GeneratePassCode(utf8string string) (string, error) {
// 	secret := base32.StdEncoding.EncodeToString([]byte(utf8string))
// 	passcode, err := totp.GenerateCodeCustom(secret, time.Now(), totp.ValidateOpts{
// 		Period:    30,
// 		Skew:      1,
// 		Digits:    otp.DigitsSix,
// 		Algorithm: otp.AlgorithmSHA512,
// 	})
// 	if err != nil {
// 		return "", err
// 	}
// 	return passcode, nil
// }

type OptToken struct {
	issuer string
	otkey  *otp.Key
}

func NewOtpToken(iss string) *OptToken {
	res := OptToken{
		issuer: iss,
	}
	return &res
}

func (ot *OptToken) GenerateKey(email string) error {
	log.Println("Generate OTP key for ", email)
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      ot.issuer,
		AccountName: email,
	})
	if err != nil {
		return err
	}
	ot.otkey = key
	return nil
}

func (ot *OptToken) GetSecret() string {
	return ot.otkey.Secret()
}

func (ot *OptToken) CreateQRFile(dir string) (string, error) {
	return writeImageFromKey(dir, ot.otkey)
}

func (ot *OptToken) CreateQRFileFromSecret(dir string, mail, secret string) (string, error) {
	url := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s", ot.issuer, mail, secret, ot.issuer)
	kk, err := otp.NewKeyFromURL(url)
	if err != nil {
		return "", err
	}
	return writeImageFromKey(dir, kk)
}

func generateTimeTrail() string {
	tt := time.Now()
	return tt.Format("20060102-150405")
}

func writeImageFromKey(dir string, kk *otp.Key) (string, error) {
	var buf bytes.Buffer
	img, err := kk.Image(200, 200)
	if err != nil {
		return "", err
	}
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	fnameQr := fmt.Sprintf("qr-code-%s.png", generateTimeTrail())
	fname := filepath.Join(dir, fnameQr)
	if err := os.WriteFile(fname, buf.Bytes(), 0644); err != nil {
		return "", err
	}

	return fnameQr, nil
}

func (ot *OptToken) CheckCode(code string, secret string) bool {
	log.Println("Checking the code ", code, secret)
	return totp.Validate(code, secret)
}
