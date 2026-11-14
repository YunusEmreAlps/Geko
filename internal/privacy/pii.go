package privacy

import (
	"regexp"
	"strings"
)

// Personal data is masked in place rather than dropping the whole field, so a
// message such as "no account for [email]" stays useful. Numbers are masked only
// when their check digits are valid, which keeps most IDs and timestamps intact.
var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}`)
	// Card numbers have 13 to 19 digits, optionally grouped by spaces or hyphens.
	// Issuers use leading digits 2 to 6, which excludes current Unix timestamps.
	cardPattern = regexp.MustCompile(`\b[2-6](?:[ -]?\d){12,18}\b`)
	// IBANs have a country code, two check digits and up to 30 characters,
	// optionally in groups of four.
	ibanPattern = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,4})?\b`)
	// Turkish identity numbers have 11 digits and never start with zero.
	tcknPattern = regexp.MustCompile(`\b[1-9]\d{10}\b`)
)

// maskPII replaces e-mail addresses, IBANs, payment card numbers and Turkish
// identity numbers with a marker naming what was removed.
func maskPII(value string) string {
	if strings.IndexByte(value, '@') >= 0 {
		value = emailPattern.ReplaceAllString(value, "[email]")
	}
	if digits(value) < 11 {
		return value
	}
	value = ibanPattern.ReplaceAllStringFunc(value, maskIBAN)
	value = cardPattern.ReplaceAllStringFunc(value, func(match string) string {
		if validCard(match) {
			return "[card]"
		}
		return match
	})
	return tcknPattern.ReplaceAllStringFunc(value, func(match string) string {
		if validTCKN(match) {
			return "[tckn]"
		}
		return match
	})
}

func digits(value string) int {
	count := 0
	for i := range len(value) {
		if value[i] >= '0' && value[i] <= '9' {
			count++
		}
	}
	return count
}

// validCard applies the Luhn check to 13 to 19 digits.
func validCard(match string) bool {
	number := strings.NewReplacer(" ", "", "-", "").Replace(match)
	if len(number) < 13 || len(number) > 19 {
		return false
	}
	sum := 0
	for i := range len(number) {
		digit := int(number[len(number)-1-i] - '0')
		if i%2 == 1 {
			if digit *= 2; digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

// maskIBAN masks the longest valid IBAN at the start of match. A following
// word in capitals, such as "DE89370400440532013000 FAILED", can extend match.
func maskIBAN(match string) string {
	for end := len(match); end >= 15; {
		if validIBAN(match[:end]) {
			return "[iban]" + match[end:]
		}
		space := strings.LastIndexByte(match[:end], ' ')
		if space < 0 {
			break
		}
		end = space
	}
	return match
}

// validIBAN applies the ISO 13616 mod-97 check.
func validIBAN(match string) bool {
	iban := strings.ToUpper(strings.ReplaceAll(match, " ", ""))
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	remainder := 0
	for _, r := range iban[4:] + iban[:4] {
		switch {
		case r >= '0' && r <= '9':
			remainder = (remainder*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			remainder = (remainder*100 + int(r-'A') + 10) % 97
		default:
			return false
		}
	}
	return remainder == 1
}

// validTCKN applies the two check digits of a Turkish identity number.
func validTCKN(number string) bool {
	var d [11]int
	for i := range d {
		d[i] = int(number[i] - '0')
	}
	odd := d[0] + d[2] + d[4] + d[6] + d[8]
	even := d[1] + d[3] + d[5] + d[7]
	if ((odd*7-even)%10+10)%10 != d[9] {
		return false
	}
	sum := 0
	for _, digit := range d[:10] {
		sum += digit
	}
	return sum%10 == d[10]
}
