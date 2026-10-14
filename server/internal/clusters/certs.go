package clusters

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"
)

// firstCert parses the first certificate in a PEM bundle.
func firstCert(pemData []byte) (*x509.Certificate, bool) {
	for {
		var block *pem.Block
		block, pemData = pem.Decode(pemData)
		if block == nil {
			return nil, false
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, false
		}
		return c, true
	}
}

// certExpiry is when the first certificate of a PEM bundle stops being valid.
func certExpiry(pemData []byte) (time.Time, bool) {
	c, ok := firstCert(pemData)
	if !ok {
		return time.Time{}, false
	}
	return c.NotAfter.UTC(), true
}

// certSubject names the first certificate's subject, as Kubernetes would
// see it: the common name, with the organizations as groups.
func certSubject(pemData []byte) (string, bool) {
	c, ok := firstCert(pemData)
	if !ok {
		return "", false
	}
	who := c.Subject.CommonName
	if len(c.Subject.Organization) > 0 {
		who += " (" + strings.Join(c.Subject.Organization, ", ") + ")"
	}
	return who, who != ""
}

// CADescription describes a CA certificate for the saved connection:
// "CN=kubernetes, valid until 2036-03-08".
func CADescription(pemData []byte) (string, error) {
	c, ok := firstCert(pemData)
	if !ok {
		return "", fmt.Errorf("not a certificate in PEM form")
	}
	name := c.Subject.CommonName
	if name == "" {
		name = c.Subject.String()
	}
	return fmt.Sprintf("CN=%s, valid until %s", name, c.NotAfter.UTC().Format("2006-01-02")), nil
}

func sortStrings(s []string) { sort.Strings(s) }
