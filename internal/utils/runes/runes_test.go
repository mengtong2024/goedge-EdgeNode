// Copyright 2023 GoEdge CDN goedge.cdn@gmail.com. All rights reserved. Official site: https://goedge.cn .

package runes_test

import (
	"github.com/TeaOSLab/EdgeNode/internal/utils/runes"
	"github.com/iwind/TeaGo/assert"
	"runtime"
	"testing"
)

func TestContainsAllWords(t *testing.T) {
	var a = assert.NewAssertion(t)
	a.IsTrue(runes.ContainsAllWords("How are you?", []string{"are", "you"}, false))
	a.IsFalse(runes.ContainsAllWords("How are you?", []string{"how", "are", "you"}, false))
	a.IsTrue(runes.ContainsAllWords("How are you?", []string{"how", "are", "you"}, true))
}

func TestContainsAnyWord(t *testing.T) {
	var a = assert.NewAssertion(t)
	a.IsTrue(runes.ContainsAnyWord("How are you?", []string{"are", "you"}, false))
	a.IsTrue(runes.ContainsAnyWord("How are you?", []string{"are", "you", "ok"}, false))
	a.IsFalse(runes.ContainsAnyWord("How are you?", []string{"how", "ok"}, false))
	a.IsTrue(runes.ContainsAnyWord("How are you?", []string{"how"}, true))
	a.IsTrue(runes.ContainsAnyWord("How are you?", []string{"how", "ok"}, true))
}

func TestContainsWordRunes(t *testing.T) {
	var a = assert.NewAssertion(t)
	a.IsFalse(runes.ContainsWordRunes([]rune(""), []rune("How"), true))
	a.IsFalse(runes.ContainsWordRunes([]rune("How are you?"), []rune(""), true))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you?"), []rune("How"), true))
	a.IsFalse(runes.ContainsWordRunes([]rune("How are you?"), []rune("how"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you?"), []rune("you"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you?"), []rune("are"), false))
	a.IsFalse(runes.ContainsWordRunes([]rune("How are you?"), []rune("re"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you w?"), []rune("w"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("w How are you?"), []rune("w"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are w you?"), []rune("w"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are how you?"), []rune("how"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you?"), []rune("how"), true))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you?"), []rune("ARE"), true))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you"), []rune("you"), false))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you"), []rune("YOU"), true))
	a.IsTrue(runes.ContainsWordRunes([]rune("How are you?"), []rune("YOU"), true))
	a.IsFalse(runes.ContainsWordRunes([]rune("How are you1?"), []rune("YOU"), true))
	a.IsFalse(runes.ContainsWordRunes([]rune("How are you1?"), []rune("YOU YOU YOU YOU YOU YOU YOU"), true))
}

func TestContainsSubRunes(t *testing.T) {
	var a = assert.NewAssertion(t)
	a.IsFalse(runes.ContainsSubRunes([]rune(""), []rune("How"), true))
	a.IsFalse(runes.ContainsSubRunes([]rune("How are you?"), []rune(""), true))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you1?"), []rune("YOU"), true))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you1?"), []rune("ow"), false))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you1?"), []rune("H"), false))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you1?"), []rune("How"), false))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you doing"), []rune("oi"), false))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you doing"), []rune("g"), false))
	a.IsTrue(runes.ContainsSubRunes([]rune("How are you doing"), []rune("ing"), false))
	a.IsFalse(runes.ContainsSubRunes([]rune("How are you doing"), []rune("int"), false))
}

func TestEqualRune(t *testing.T) {
	var a = assert.NewAssertion(t)
	a.IsTrue(runes.EqualRune('a', 'a', false))
	a.IsTrue(runes.EqualRune('a', 'a', true))
	a.IsFalse(runes.EqualRune('a', 'A', false))
	a.IsTrue(runes.EqualRune('a', 'A', true))
	a.IsFalse(runes.EqualRune('c', 'C', false))
	a.IsTrue(runes.EqualRune('c', 'C', true))
	a.IsTrue(runes.EqualRune('C', 'C', true))
	a.IsTrue(runes.EqualRune('C', 'c', true))
	a.IsTrue(runes.EqualRune('Z', 'z', true))
	a.IsTrue(runes.EqualRune('z', 'Z', true))
	a.IsFalse(runes.EqualRune('z', 'z'+('a'-'A'), true))
}

func BenchmarkContainsWordRunes(b *testing.B) {
	runtime.GOMAXPROCS(4)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = runes.ContainsWordRunes([]rune("How are you"), []rune("YOU"), true)
		}
	})
}

func BenchmarkContainsSubRunes(b *testing.B) {
	runtime.GOMAXPROCS(4)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = runes.ContainsSubRunes([]rune("How are you"), []rune("YOU"), true)
		}
	})
}
