package testing

import (
	"io"
	"io/ioutil"
	"os"
	"strings"
	"testing"
)

const FakeCA = `-----BEGIN CERTIFICATE-----
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
1w==
-----END CERTIFICATE-----
`

const FakeCert = `-----BEGIN CERTIFICATE-----
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
1w==
-----END CERTIFICATE-----
`

const FakeKey = `-----BEGIN PRIVATE KEY-----
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
r3SYMVxpjisC8IFQCqaRSfWoNFkMYbY=
-----END PRIVATE KEY-----
`

func tempData(t testing.TB, data string) (string, func()) {
	t.Helper()
	tf, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer tf.Close()
	if _, err := io.Copy(tf, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return tf.Name(), func() {
		if err := os.Remove(tf.Name()); err != nil {
			t.Error(err)
		}
	}
}

func WithFakeCerts(t testing.TB) (caPath, certPath, keyPath string, cleanup func()) {
	t.Helper()

	caPath, caClean := tempData(t, FakeCA)
	certPath, certClean := tempData(t, FakeCert)
	keyPath, keyClean := tempData(t, FakeKey)

	return caPath, certPath, keyPath, func() {
		defer caClean()
		defer certClean()
		defer keyClean()
	}
}
