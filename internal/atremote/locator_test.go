package atremote

import "testing"

func TestLocatorRoundTripsOnlyValidKeys(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		valid bool
	}{
		{name: "lowercase", key: "esp32", valid: true},
		{name: "digits and dashes", key: "esp32-c3-1", valid: true},
		{name: "single character", key: "a", valid: true},
		{name: "maximum length", key: "a123456789012345678901234567890", valid: true},
		{name: "empty", key: "", valid: false},
		{name: "leading dash", key: "-esp32", valid: false},
		{name: "uppercase", key: "ESP32", valid: false},
		{name: "underscore", key: "esp_32", valid: false},
		{name: "dot", key: "esp.32", valid: false},
		{name: "colon", key: "esp:32", valid: false},
		{name: "slash", key: "esp/32", valid: false},
		{name: "too long", key: "a1234567890123456789012345678901", valid: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if ValidKey(testCase.key) != testCase.valid {
				t.Fatalf("ValidKey(%q) = %v, want %v", testCase.key, !testCase.valid, testCase.valid)
			}
			locator := Locator(testCase.key)
			if !testCase.valid {
				if locator != "" {
					t.Fatalf("Locator(%q) = %q, want empty", testCase.key, locator)
				}
				return
			}
			if locator != EndpointScheme+testCase.key {
				t.Fatalf("Locator(%q) = %q", testCase.key, locator)
			}
			parsed, ok := ParseLocator(locator)
			if !ok || parsed != testCase.key {
				t.Fatalf("ParseLocator(%q) = %q, %v", locator, parsed, ok)
			}
		})
	}
}

func TestLocatorRoutingPredicateSeparatesTransports(t *testing.T) {
	for _, endpoint := range []string{"/dev/ttyUSB2", "/dev/ttyACM0", "", "at-bridg:esp32", "http://192.168.10.11"} {
		if IsLocator(endpoint) {
			t.Fatalf("IsLocator(%q) = true, want false", endpoint)
		}
	}
	// A bridge-scheme endpoint with an invalid key must still route to the
	// bridge transport, which rejects it. Routing must not depend on key
	// validity, otherwise a typo would silently reach the local tty transport.
	if !IsLocator(EndpointScheme + "BAD_KEY") {
		t.Fatal("bridge-scheme endpoint with an invalid key must route to the bridge transport")
	}
	if _, ok := ParseLocator(EndpointScheme + "BAD_KEY"); ok {
		t.Fatal("ParseLocator accepted an invalid key")
	}
}
