package tls

import (
	"testing"

	cryptotls "crypto/tls"
)

func TestMapCurveNamesToIDs(t *testing.T) {
	tests := []struct {
		name   string
		input  []string
		want   []cryptotls.CurveID
		wantEr bool
	}{
		{
			name:  "nil input",
			input: nil,
			want:  nil,
		},
		{
			name:  "IANA names",
			input: []string{"X25519", "secp256r1", "secp384r1", "secp521r1"},
			want:  []cryptotls.CurveID{cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384, cryptotls.CurveP521},
		},
		{
			name:  "Go constant name aliases",
			input: []string{"CurveP256", "CurveP384", "CurveP521"},
			want:  []cryptotls.CurveID{cryptotls.CurveP256, cryptotls.CurveP384, cryptotls.CurveP521},
		},
		{
			name:  "post-quantum hybrids",
			input: []string{"X25519MLKEM768", "SecP256r1MLKEM768", "SecP384r1MLKEM1024"},
			want:  []cryptotls.CurveID{cryptotls.X25519MLKEM768, cryptotls.SecP256r1MLKEM768, cryptotls.SecP384r1MLKEM1024},
		},
		{
			name:   "unknown name fails closed",
			input:  []string{"X25519", "bogusCurve"},
			wantEr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapCurveNamesToIDs(tt.input)
			if tt.wantEr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
