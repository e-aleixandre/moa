package book

import (
	"strings"
	"testing"
)

// The contraexamples the review reproduced, as literal fixtures: each one is a
// ranking the previous scorer got backwards.

// Score decides, not what the file is. A work file whose title is the query is
// the answer; ranking it under a sheet with one passing mention hides exactly
// the work in progress the owner asks about.
func TestSearchScoreBeatsKind(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "work/whatsapp.md", `---
title: WhatsApp proveedor
---

Integración de WhatsApp con el proveedor, en curso.
`)
	writeSheet(t, dir, "areas/erp/old.md", `---
title: ERP antiguo
---

Módulo heredado. Alguna vez se habló de whatsapp para avisar al proveedor,
pero no se hizo nada.
`)
	hits, _, err := SearchBook(dir, "whatsapp proveedor", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "work/whatsapp.md" {
		t.Fatalf("kind outranked score: %+v", hits)
	}
}

// Length normalizes the body only. Counting the field boosts as length made a
// long sheet titled exactly like the query look bloated and lose to a short
// one that mentions the word once.
func TestSearchTitleWinsOverALongBody(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/erp/albaran.md", "---\ntitle: Albarán\n---\n\n"+
		strings.Repeat("Detalle del proceso, con pasos, tablas y servicios descritos al completo.\n", 60))
	writeSheet(t, dir, "areas/otros/corto.md", "---\ntitle: Otra cosa\n---\n\nAquí se nombra un albarán.\n")

	hits, _, err := SearchBook(dir, "albaran", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "areas/erp/albaran.md" {
		t.Fatalf("a long sheet lost its own title: %+v", hits)
	}
}

// Plural and singular are the same question, in both directions, and a short
// word like "api" must reach "APIs" without lowering the prefix threshold.
func TestSearchMatchesPluralsBothWays(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/erp/albaran.md", "---\ntitle: Albarán\naliases: albarán\n---\n\nUn albarán del proveedor.\n")
	writeSheet(t, dir, "areas/web/apis.md", "---\ntitle: APIs públicas\n---\n\nLas APIs que expone el producto.\n")
	writeSheet(t, dir, "areas/erp/pedidos.md", "---\ntitle: Pedidos\n---\n\nPedido de compra.\n")

	for _, tc := range []struct{ query, want string }{
		{"albaranes", "areas/erp/albaran.md"}, // plural query, singular sheet
		{"albaran", "areas/erp/albaran.md"},   // no accent, accented title
		{"api", "areas/web/apis.md"},          // short singular, plural sheet
		{"pedido", "areas/erp/pedidos.md"},
		{"pedidos", "areas/erp/pedidos.md"},
	} {
		hits, _, err := SearchBook(dir, tc.query, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Path != tc.want {
			t.Fatalf("query %q = %+v, want %s first", tc.query, hits, tc.want)
		}
	}
}

// A short token still must not match by prefix in either direction: "usu"
// would pull in every sheet that says "usuario".
func TestSearchShortTokenStillDoesNotMatchByPrefix(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/a/usuarios.md", "---\ntitle: Usuarios\n---\n\ngestión de usuarios\n")
	if hits, _, _ := SearchBook(dir, "usu", 5); len(hits) != 0 {
		t.Fatalf("short token matched by prefix: %+v", hits)
	}
}

// "pedidos sin factura" and "pedidos con factura" are different questions.
// Dropping the negation as a stopword made the ranker answer the opposite one.
func TestSearchKeepsNegations(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/erp/sin-factura.md", "---\ntitle: Pedidos sin factura\n---\n\nPedidos que se sirven sin factura.\n")
	writeSheet(t, dir, "areas/erp/con-factura.md", "---\ntitle: Pedidos con factura\n---\n\nPedidos facturados al cerrar.\n")

	terms := queryTerms("pedidos sin factura")
	var got []string
	for _, term := range terms {
		got = append(got, term.text)
	}
	if strings.Join(got, ",") != "pedidos,sin,factura" {
		t.Fatalf("the negation was dropped from the query: %v", got)
	}
	hits, _, err := SearchBook(dir, "pedidos sin factura", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "areas/erp/sin-factura.md" {
		t.Fatalf("the negated question ranked its opposite first: %+v", hits)
	}
}

// work/ files say how long ago they moved: "what is stopped and since when" is
// asked of the owner on every turn.
func TestListShowsHowLongAgoWorkMoved(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "work/parked.md", "---\ntitle: Parked\n---\n\nx\n")
	writeSheet(t, dir, "areas/a/sheet.md", "---\ntitle: Sheet\n---\n\nx\n")
	touch(t, dir, "work/parked.md", -9*24)

	listed := resultText(run(t, NewTool(dir, true), map[string]any{"action": "list"}))
	if !strings.Contains(listed, "work/parked.md — Parked (moved 9 days ago)") {
		t.Fatalf("list = %q", listed)
	}
	if strings.Contains(listed, "areas/a/sheet.md — Sheet (moved") {
		t.Fatalf("an area sheet carried a modification time: %q", listed)
	}
}
