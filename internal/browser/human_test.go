package browser

import (
	"testing"
)

func TestHumanKeySpecAndButtonMask(t *testing.T) {
	// Verify shifted and unshifted US-QWERTY key specs.
	ksA, ok := lookupKeySpec('A')
	if !ok || !ksA.Shift || ksA.Code != "KeyA" || ksA.VK != 65 {
		t.Fatalf("lookupKeySpec('A') = %+v, ok=%v", ksA, ok)
	}
	ksa, ok := lookupKeySpec('a')
	if !ok || ksa.Shift || ksa.Code != "KeyA" || ksa.VK != 65 {
		t.Fatalf("lookupKeySpec('a') = %+v, ok=%v", ksa, ok)
	}
	ksBang, ok := lookupKeySpec('!')
	if !ok || !ksBang.Shift || ksBang.Code != "Digit1" || ksBang.VK != 49 {
		t.Fatalf("lookupKeySpec('!') = %+v, ok=%v", ksBang, ok)
	}

	if buttonMask("left") != 1 || buttonMask("right") != 2 || buttonMask("middle") != 4 {
		t.Fatal("unexpected buttonMask values")
	}

	for i := 0; i < 50; i++ {
		v := randTriangular(10.0)
		if v < -10.0 || v > 10.0 {
			t.Fatalf("randTriangular(10) out of bounds: %v", v)
		}
	}
}
