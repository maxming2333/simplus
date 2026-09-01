package modemadapter

import (
	"strings"
	"testing"
)

type fixedIdentityPseudonymizer struct{ value string }

func (pseudonymizer fixedIdentityPseudonymizer) Pseudonym(string, []byte) (string, error) {
	return pseudonymizer.value, nil
}

func TestML307AICCIDIsOnlyReturnedAsAKeyedPseudonymAndMaskedHint(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	actual, hint := pseudonymizedICCID(
		[]string{"+MCCID: 89861118216007272115", "OK"},
		"+MCCID:",
		fixedIdentityPseudonymizer{value: fingerprint},
	)
	if actual != fingerprint || hint != "ICCID •••• 2115" {
		t.Fatalf("identity = (%q, %q)", actual, hint)
	}
	for _, lines := range [][]string{
		{"+MCCID: not-an-iccid", "OK"},
		{"+MCCID: 12345678901234", "OK"},
		{"89861118216007272115", "OK"},
		{"+MCCID: 89861118216007272115", "ERROR"},
	} {
		if value, display := pseudonymizedICCID(lines, "+MCCID:", fixedIdentityPseudonymizer{value: fingerprint}); value != "" || display != "" {
			t.Fatalf("invalid identity was accepted: (%q, %q)", value, display)
		}
	}
}

// TestICCIDPadNibbleIsNotPartOfTheStableIdentity covers cards whose ICCID has an
// odd digit count: EF_ICCID pads the unused BCD nibble with 'F', and a modem that
// reports the field verbatim returns it. The pad must not reach the fingerprint
// input, otherwise one card's stable SIM identity would depend on its digit
// count, and the masked hint would end in the pad instead of real digits.
func TestICCIDPadNibbleIsNotPartOfTheStableIdentity(t *testing.T) {
	fingerprint := strings.Repeat("b", 64)
	pseudonymizer := fixedIdentityPseudonymizer{value: fingerprint}
	for _, testCase := range []struct {
		name     string
		reported string
		wantHint string
	}{
		{name: "nineteen digits with one pad", reported: "8901260228723190867F", wantHint: "ICCID •••• 0867"},
		{name: "lowercase pad", reported: "8901260228723190867f", wantHint: "ICCID •••• 0867"},
		{name: "eighteen digits with two pads", reported: "890126022872319086FF", wantHint: "ICCID •••• 9086"},
		{name: "unpadded", reported: "89861118216007272115", wantHint: "ICCID •••• 2115"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value, hint := pseudonymizedICCID([]string{"+MCCID: " + testCase.reported, "OK"}, "+MCCID:", pseudonymizer)
			if value != fingerprint || hint != testCase.wantHint {
				t.Fatalf("identity = (%q, %q), want hint %q", value, hint, testCase.wantHint)
			}
		})
	}
	// The pad is trimmed, so both spellings of the same card must fingerprint the
	// same bytes. A recording pseudonymizer proves the input, not just the output.
	recorder := &recordingIdentityPseudonymizer{value: fingerprint}
	pseudonymizedICCID([]string{"+MCCID: 8901260228723190867F", "OK"}, "+MCCID:", recorder)
	pseudonymizedICCID([]string{"+MCCID: 8901260228723190867", "OK"}, "+MCCID:", recorder)
	if len(recorder.inputs) != 2 || recorder.inputs[0] != recorder.inputs[1] {
		t.Fatalf("pad nibble reached the fingerprint input: %#v", recorder.inputs)
	}
	// Anything that is not a trailing BCD pad stays invalid.
	for _, reported := range []string{"8901F60228723190867", "89012602287231908FFF", "FFFFFFFFFFFFFFFFFFFF"} {
		if value, hint := pseudonymizedICCID([]string{"+MCCID: " + reported, "OK"}, "+MCCID:", pseudonymizer); value != "" || hint != "" {
			t.Fatalf("invalid identity %q was accepted: (%q, %q)", reported, value, hint)
		}
	}
}

type recordingIdentityPseudonymizer struct {
	value  string
	inputs []string
}

func (pseudonymizer *recordingIdentityPseudonymizer) Pseudonym(_ string, value []byte) (string, error) {
	pseudonymizer.inputs = append(pseudonymizer.inputs, string(value))
	return pseudonymizer.value, nil
}

func TestML307AIMEIParserRequiresAValidCheckDigitAndTerminalResponse(t *testing.T) {
	lines := []string{"+CGSN: 490154203237518", "OK"}
	if actual := equipmentIMEI(lines); actual != "490154203237518" {
		t.Fatalf("IMEI = %q", actual)
	}
	for _, invalid := range [][]string{
		{"+CGSN: 490154203237519", "OK"},
		{"+CGSN: 490154203237518", "ERROR"},
		{"4901542032375180", "OK"},
	} {
		if actual := equipmentIMEI(invalid); actual != "" {
			t.Fatalf("invalid IMEI was accepted: %q", actual)
		}
	}
}
