package modemadapter

import (
	"regexp"
	"strings"

	"github.com/leonfox28/simplus/internal/attransport"
)

var (
	iccidPattern       = regexp.MustCompile(`^[0-9]{18,22}$`)
	imeiPattern        = regexp.MustCompile(`^[0-9]{15}$`)
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func pseudonymizedICCID(lines []string, responsePrefix string, pseudonymizer IdentityPseudonymizer) (string, string) {
	if pseudonymizer == nil || !attransport.HasTerminalOK(lines) {
		return "", ""
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if responsePrefix == "" || !strings.HasPrefix(line, responsePrefix) {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, responsePrefix)), `"`)
		value = trimICCIDPadding(value)
		if !iccidPattern.MatchString(value) {
			return "", ""
		}
		fingerprint, err := pseudonymizer.Pseudonym("sim-iccid-v1", []byte(value))
		if err != nil || !fingerprintPattern.MatchString(fingerprint) {
			return "", ""
		}
		return fingerprint, "ICCID •••• " + value[len(value)-4:]
	}
	return "", ""
}

// trimICCIDPadding removes the BCD pad nibble that EF_ICCID carries when the
// ICCID has an odd digit count.
//
// EF_ICCID is 10 bytes, so it holds 20 BCD nibbles. An ITU-T E.118 identifier
// shorter than that is padded with 'F' in the unused low nibbles, and a modem
// that reports the field verbatim returns those pads. The pad is not part of the
// identifier, so it is removed before validation and before pseudonymization:
// keeping it would make one card's stable SIM fingerprint depend on whether its
// digit count happened to be odd.
//
// Only trailing pads are removed. A value with an 'F' anywhere else is not a
// padded identifier and stays invalid.
func trimICCIDPadding(value string) string {
	trimmed := strings.TrimRight(value, "Ff")
	if len(value)-len(trimmed) > 2 {
		// More pad nibbles than EF_ICCID can produce for a valid identifier.
		return value
	}
	return trimmed
}

func equipmentIMEI(lines []string) string {
	if !attransport.HasTerminalOK(lines) {
		return ""
	}
	for _, line := range lines {
		value := strings.TrimSpace(line)
		if strings.HasPrefix(value, "+CGSN:") {
			value = strings.Trim(strings.TrimSpace(strings.TrimPrefix(value, "+CGSN:")), `"`)
		}
		if !imeiPattern.MatchString(value) || !validIMEICheckDigit(value) {
			continue
		}
		return value
	}
	return ""
}

func validIMEICheckDigit(value string) bool {
	if !imeiPattern.MatchString(value) {
		return false
	}
	sum := 0
	for index, character := range value {
		digit := int(character - '0')
		if index%2 == 1 {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}
