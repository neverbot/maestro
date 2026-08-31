package testutil

import "testing"

func TestReplaceDBName(t *testing.T) {
	cases := []struct {
		name    string
		rawURL  string
		dbName  string
		want    string
		wantErr bool
	}{
		{
			name:   "query parameter containing a slash",
			rawURL: "postgres://u:p@host/postgres?sslmode=verify-full&sslrootcert=/etc/ssl/ca.pem",
			dbName: "maestro_test_abc123",
			want:   "postgres://u:p@host/maestro_test_abc123?sslmode=verify-full&sslrootcert=/etc/ssl/ca.pem",
		},
		{
			name:   "url with no path",
			rawURL: "postgres://u:p@host:5432",
			dbName: "maestro_test_def456",
			want:   "postgres://u:p@host:5432/maestro_test_def456",
		},
		{
			name:   "password containing an encoded slash",
			rawURL: "postgres://user:p%2Fss@host:5432/postgres?sslmode=disable",
			dbName: "maestro_test_ghi789",
			want:   "postgres://user:p%2Fss@host:5432/maestro_test_ghi789?sslmode=disable",
		},
		{
			name:    "keyword/value DSN is rejected rather than mangled",
			rawURL:  "host=localhost port=5432 user=postgres dbname=postgres sslmode=disable",
			dbName:  "maestro_test_jkl012",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := replaceDBName(tc.rawURL, tc.dbName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("replaceDBName(%q) = %q, nil; want an error", tc.rawURL, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("replaceDBName(%q): %v", tc.rawURL, err)
			}
			if got != tc.want {
				t.Fatalf("replaceDBName(%q) = %q, want %q", tc.rawURL, got, tc.want)
			}
		})
	}
}
