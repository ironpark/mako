package gui

import "slices"

// Hangul in fuzzy matching. A Korean word is typed a jamo at a time, and
// the input method composes them into syllables as it goes, so what is
// typed partway seldom equals what it will become: "세션" is "셋" a key
// before its end, its last consonant taken as a final until the vowel
// after moves it on. So the last rune of a query matches by its keys: it
// matches where they begin the keys of the syllable there and the next.
// A lone consonant elsewhere in a query is an initial, so "ㅅㅅ" finds
// "세션" too.

const (
	syllableBase = 0xAC00
	syllableLast = 0xD7A3
	jungCount    = 21
	jongCount    = 28
)

var (
	choRunes  = []rune("ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ")
	jungRunes = []rune("ㅏㅐㅑㅒㅓㅔㅕㅖㅗㅘㅙㅚㅛㅜㅝㅞㅟㅠㅡㅢㅣ")
	jongRunes = []rune(" ㄱㄲㄳㄴㄵㄶㄷㄹㄺㄻㄼㄽㄾㄿㅀㅁㅂㅄㅅㅆㅇㅈㅊㅋㅌㅍㅎ")
	// compounds are compoundKeys as runes.
	compounds = map[rune][]rune{}
	// compoundKeys are the jamo typed as two keys: the compound vowels and
	// the compound finals.
	compoundKeys = map[rune]string{
		'ㅘ': "ㅗㅏ", 'ㅙ': "ㅗㅐ", 'ㅚ': "ㅗㅣ", 'ㅝ': "ㅜㅓ", 'ㅞ': "ㅜㅔ", 'ㅟ': "ㅜㅣ", 'ㅢ': "ㅡㅣ",
		'ㄳ': "ㄱㅅ", 'ㄵ': "ㄴㅈ", 'ㄶ': "ㄴㅎ", 'ㄺ': "ㄹㄱ", 'ㄻ': "ㄹㅁ", 'ㄼ': "ㄹㅂ",
		'ㄽ': "ㄹㅅ", 'ㄾ': "ㄹㅌ", 'ㄿ': "ㄹㅍ", 'ㅀ': "ㄹㅎ", 'ㅄ': "ㅂㅅ",
	}
)

func init() {
	for r, k := range compoundKeys {
		compounds[r] = []rune(k)
	}
}

// keys appends the jamo keys r is typed with: a syllable's initial, vowel
// and final, a compound as its two, or a lone jamo itself; nothing for a
// rune that is not Hangul.
func keys(out []rune, r rune) []rune {
	key := func(j rune) {
		if c, ok := compounds[j]; ok {
			out = append(out, c...)
		} else if j != ' ' {
			out = append(out, j)
		}
	}
	switch {
	case r >= syllableBase && r <= syllableLast:
		i := int(r - syllableBase)
		key(choRunes[i/(jungCount*jongCount)])
		key(jungRunes[i/jongCount%jungCount])
		key(jongRunes[i%jongCount])
	case r >= 'ㄱ' && r <= 'ㅣ':
		key(r)
	}
	return out
}

// initial is a syllable's initial consonant, 0 for any other rune.
func initial(r rune) rune {
	if r < syllableBase || r > syllableLast {
		return 0
	}
	return choRunes[int(r-syllableBase)/(jungCount*jongCount)]
}

// matchHangul reports how many runes of s, from i, the query rune q takes,
// by its keys: the query's last rune, whose keys are want, takes the
// syllable its keys begin, and the next too if they reach into it; a lone
// consonant before it, with want nil, takes a syllable it begins. It
// returns 0 for none, leaving every other rune to the plain match.
func matchHangul(s []rune, i int, q rune, want []rune) int {
	if want == nil {
		if q >= 'ㄱ' && q <= 'ㅎ' && initial(s[i]) == q {
			return 1
		}
		return 0
	}
	if len(want) == 0 {
		return 0
	}
	var buf [10]rune // a syllable is at most 5 keys, and the next too
	have := keys(buf[:0], s[i])
	n := len(have)
	if n == 0 {
		return 0
	}
	if i+1 < len(s) {
		have = keys(have, s[i+1])
	}
	if len(want) > len(have) || !slices.Equal(want, have[:len(want)]) {
		return 0
	}
	return pick(len(want) > n, 2, 1)
}
