package step

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPositiveInt(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr string
	}{
		{
			name: "empty means the default",
			raw:  "",
			want: 0,
		},
		{
			name: "a plain number parses",
			raw:  "2048",
			want: 2048,
		},
		{
			name:    "not a number",
			raw:     "2GB",
			wantErr: `emulator_ram_mb must be a whole number, got "2GB"`,
		},
		{
			name:    "zero is not a usable value",
			raw:     "0",
			wantErr: "emulator_ram_mb must be positive, got 0",
		},
		{
			name:    "negative",
			raw:     "-4",
			wantErr: "emulator_ram_mb must be positive, got -4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := positiveInt("emulator_ram_mb", tt.raw)

			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHasEmulatorConfig(t *testing.T) {
	require.False(t, hasEmulatorConfig(Config{}))
	require.True(t, hasEmulatorConfig(Config{SystemImage: "system-images;android-34;google_apis;x86_64"}))
	require.True(t, hasEmulatorConfig(Config{EmulatorRAMMB: 4096}))
	require.True(t, hasEmulatorConfig(Config{EmulatorCores: 4}))
	require.True(t, hasEmulatorConfig(Config{EmulatorColdBoot: true}))
}
