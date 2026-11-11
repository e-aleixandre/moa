package dreaming

import (
	"strings"
	"testing"
)

// A decision file with two independent agreements; a later decision of the
// user supersedes only the first.
var decisionOrig = []byte("# Precios\n## 1. Margen\nEl margen es 30 % (10/11).\n## 2. Redondeo\nSe redondea a 0,50 (10/11).\n")

func decisionInput(plan Plan) AssembleInput {
	return AssembleInput{
		Unit: Unit{Sheet: "decisions/precios.md", Files: []string{"decisions/precios.md"}},
		Orig: [][]byte{decisionOrig}, Plan: plan, Date: "2026-11-12", Nonce: "n0nce", S0: "abcdef1234",
		SourceExists: func(path string, line int) bool {
			return (path == "decisions/margen-2027.md" && line >= 1 && line <= 4) ||
				(path == "work/precios.md" && line == 7) || (path == "decisions/precios.md" && line <= 5)
		},
	}
}

func supersedeFirst() Plan {
	return Plan{
		State: "> Superada por `decisions/margen-2027.md` (11/11): decisions/precios.md:2-3",
		Keep:  [][3]int{{0, 4, 5}},
		Archive: []Archived{{File: 0, Lines: [2]int{2, 3}, Reason: ReasonSuperseded,
			Source: "decisions/margen-2027.md:3", Source2: "work/precios.md:7"}},
	}
}

// Superseding one agreement archives only its lines, verbatim, keeps the
// other, cites both sources, and the result proves every original byte.
func TestDecisionIsSupersededByRangeAndKeepsTheRest(t *testing.T) {
	in := decisionInput(supersedeFirst())
	out, err := Assemble(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(in.Unit, in.Orig, out, in.Nonce); err != nil {
		t.Fatal(err)
	}
	sheet := string(out.Files["decisions/precios.md"])
	if !strings.Contains(sheet, "Se redondea a 0,50 (10/11).") || strings.Contains(sheet, "El margen es 30 %") {
		t.Fatalf("sheet:\n%s", sheet)
	}
	if strings.Contains(sheet, "## Estado actual") || strings.Contains(sheet, "Detalle vigente") {
		t.Fatalf("a decision got generated prose:\n%s", sheet)
	}
	var archive string
	for p, c := range out.Files {
		if strings.HasPrefix(p, "archivo/") {
			archive = string(c)
		}
	}
	if !strings.Contains(archive, "El margen es 30 % (10/11).") || !strings.Contains(archive, "fuente2=S0:") {
		t.Fatalf("archive:\n%s", archive)
	}
}

func TestDecisionRetirementRules(t *testing.T) {
	cases := map[string]func(p *Plan){
		"closed is not superseded":        func(p *Plan) { p.Archive[0].Reason = "cerrado" },
		"repeated is not superseded":      func(p *Plan) { p.Archive[0].Reason = "repetido" },
		"unknown outcome cannot retire":   func(p *Plan) { p.Archive[0].Reason = "por_confirmar" },
		"a sheet is not a decision":       func(p *Plan) { p.Archive[0].Source = "work/precios.md:7" },
		"the decision cannot cite itself": func(p *Plan) { p.Archive[0].Source = "decisions/precios.md:5" },
		"no second citation":              func(p *Plan) { p.Archive[0].Source2 = "" },
		"second citation equals first":    func(p *Plan) { p.Archive[0].Source2 = p.Archive[0].Source },
		"second citation not in S0":       func(p *Plan) { p.Archive[0].Source2 = "work/precios.md:99" },
		"normative prose in the state": func(p *Plan) {
			p.State += "\nEl margen pasa a ser del 35 %."
		},
		"a state section in a decision": func(p *Plan) { p.State = "## Estado actual\n- margen 35 %" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := supersedeFirst()
			mutate(&p)
			if _, err := Assemble(decisionInput(p)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
