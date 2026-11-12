package dreaming

import (
	"bytes"
	"strings"
	"testing"
)

func exists(string, int) bool { return true }

func sampleUnit() (Unit, [][]byte) {
	sheet := "---\naliases: [katxina]\nverified_at: 2026-11-01\n---\n\n# Pedidos Ikili\r\n\nl7 viejo\nl8 viejo\nl9 vigente\nrepetida\nrepetida\nsin salto final"
	part := "<!-- cabecera -->\n<!-- fin-cabecera -->\nparte vieja\nparte vigente\n"
	return Unit{Sheet: "work/x.md", Files: []string{"work/x.md", "work/x/parte-01.md"}}, [][]byte{[]byte(sheet), []byte(part)}
}

func samplePlan() Plan {
	return Plan{
		State: "## Estado actual\n- algo (11-nov · detalle abajo)\n- por confirmar: work/x/parte-01.md:3-3",
		Keep:  [][3]int{{0, 7, 7}, {0, 10, 13}, {1, 4, 4}},
		Archive: []Archived{
			{File: 0, Lines: [2]int{8, 9}, Reason: ReasonSuperseded, Source: "work/x.md:10"},
			{File: 1, Lines: [2]int{1, 2}, Reason: ReasonRepeated, Source: "work/x.md:1"},
			{File: 1, Lines: [2]int{3, 3}, Reason: ReasonUnconfirmed},
		},
	}
}

func assemble(t *testing.T, u Unit, orig [][]byte, p Plan) (Output, error) {
	t.Helper()
	return Assemble(AssembleInput{Unit: u, Orig: orig, Plan: p, Date: "2026-11-12", Nonce: "n0nce", S0: "abc", SourceExists: exists})
}

func TestAssembleRebuildsEveryFileByteForByte(t *testing.T) {
	u, orig := sampleUnit()
	out, err := assemble(t, u, orig, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(out.Files["work/x.md"])
	if !strings.HasPrefix(sheet, "---\naliases: [katxina]\nverified_at: 2026-11-01\n---\n\n# Pedidos Ikili\r\n") {
		t.Fatalf("header and title not kept verbatim at the top:\n%s", sheet)
	}
	if len(out.Remove) != 1 || out.Remove[0] != "work/x/parte-01.md" {
		t.Fatalf("remove = %v", out.Remove)
	}
	if _, ok := out.Files["archivo/work/x/2026-11-12.md"]; !ok {
		t.Fatalf("archive missing: %v", keys(out.Files))
	}
	if err := Verify(u, orig, out, "n0nce"); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Every corruption of the written bytes or of their metadata must be caught by
// the guard, which reads only what would be written.
func TestVerifyRejectsCorruptedOutput(t *testing.T) {
	u, orig := sampleUnit()
	good, err := assemble(t, u, orig, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	arch := "archivo/work/x/2026-11-12.md"
	cases := map[string]func(o *Output){
		"line dropped from the sheet": func(o *Output) {
			o.Files[u.Sheet] = bytes.Replace(o.Files[u.Sheet], []byte("l9 vigente\n"), []byte(""), 1)
		},
		"line changed in the archive": func(o *Output) {
			o.Files[arch] = bytes.Replace(o.Files[arch], []byte("l8 viejo"), []byte("l8 nuevo"), 1)
		},
		"two lines swapped": func(o *Output) {
			o.Files[arch] = bytes.Replace(o.Files[arch], []byte("l7 viejo\nl8 viejo\n"), []byte("l8 viejo\nl7 viejo\n"), 1)
		},
		"archive file lost": func(o *Output) { delete(o.Files, arch) },
		"CRLF normalized": func(o *Output) {
			o.Files[u.Sheet] = bytes.Replace(o.Files[u.Sheet], []byte("\r\n"), []byte("\n"), 1)
		},
		"final newline swallowed into the payload": func(o *Output) {
			o.Files[u.Sheet] = bytes.Replace(o.Files[u.Sheet], []byte("len=44"), []byte("len=45"), 1)
		},
		"len metadata wrong": func(o *Output) {
			o.Files[arch] = bytes.Replace(o.Files[arch], []byte("len=18"), []byte("len=9"), 1)
		},
		"line range metadata wrong": func(o *Output) {
			o.Files[arch] = bytes.Replace(o.Files[arch], []byte("l=8-9"), []byte("l=8-8"), 1)
		},
		"file metadata wrong": func(o *Output) {
			o.Files[arch] = bytes.Replace(o.Files[arch], []byte("f=1 l=3-3"), []byte("f=0 l=3-3"), 1)
		},
		"block duplicated in sheet and archive": func(o *Output) {
			o.Files[u.Sheet] = append(o.Files[u.Sheet], []byte("<!-- dreaming:n0nce f=0 l=8-9 len=18 -->\nl8 viejo\nl9 viejo\n")...)
		},
		"header archived": func(o *Output) {
			o.Files[u.Sheet] = bytes.Replace(o.Files[u.Sheet], []byte("aliases: [katxina]\n"), nil, 1)
		},
		"a part removed that is not in the unit": func(o *Output) { o.Remove = append(o.Remove, "decisions/a.md") },
		"writes outside the archive":             func(o *Output) { o.Files["decisions/a.md"] = []byte("x") },
	}
	if !bytes.Contains(good.Files[arch], []byte("len=18")) || !bytes.Contains(good.Files[u.Sheet], []byte("l=10-13 len=44")) {
		t.Fatalf("fixture changed; archive:\n%s", good.Files[arch])
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			o := Output{Files: map[string][]byte{}, Remove: append([]string(nil), good.Remove...)}
			for k, v := range good.Files {
				o.Files[k] = append([]byte(nil), v...)
			}
			corrupt(&o)
			if err := Verify(u, orig, o, "n0nce"); err == nil {
				t.Fatal("guard accepted a corrupted output")
			}
		})
	}
}

func TestAssembleRejectsUnprovablePlans(t *testing.T) {
	u, orig := sampleUnit()
	cases := map[string]func(p *Plan){
		"line left unclaimed":     func(p *Plan) { p.Keep = p.Keep[1:] },
		"line kept and archived":  func(p *Plan) { p.Keep = append(p.Keep, [3]int{0, 8, 8}) },
		"range outside the file":  func(p *Plan) { p.Keep = append(p.Keep, [3]int{1, 5, 9}) },
		"header in a range":       func(p *Plan) { p.Keep = append(p.Keep, [3]int{0, 2, 2}) },
		"no estado actual":        func(p *Plan) { p.State = "## Otra cosa" },
		"por_confirmar no bullet": func(p *Plan) { p.State = "## Estado actual\n- algo" },
		"reason without source":   func(p *Plan) { p.Archive[0].Source = "" },
		"unknown reason":          func(p *Plan) { p.Archive[0].Reason = "viejo" },
		"file not in unit":        func(p *Plan) { p.Archive[0].File = 7 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := samplePlan()
			p.Archive = append([]Archived(nil), p.Archive...)
			mutate(&p)
			if _, err := assemble(t, u, orig, p); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("source missing from the snapshot", func(t *testing.T) {
		_, err := Assemble(AssembleInput{Unit: u, Orig: orig, Plan: samplePlan(), Date: "2026-11-12", Nonce: "n0nce", S0: "abc",
			SourceExists: func(string, int) bool { return false }})
		if err == nil {
			t.Fatal("accepted a source that is not in S0")
		}
	})
	t.Run("nonce occurs in the original", func(t *testing.T) {
		o := [][]byte{append([]byte("n0nce\n"), orig[0]...), orig[1]}
		if _, err := assemble(t, u, o, samplePlan()); err == nil {
			t.Fatal("accepted")
		}
	})
}

// A second night reads a sheet that already carries markers of a previous
// nonce: they are plain content for the new guard and survive byte for byte.
func TestSecondNightTreatsOldMarkersAsContent(t *testing.T) {
	u, orig := sampleUnit()
	first, err := assemble(t, u, orig, samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	u2 := Unit{Sheet: u.Sheet, Files: []string{u.Sheet}}
	o2 := [][]byte{first.Files[u.Sheet]}
	n := len(splitLines(o2[0]))
	h := headerLen(splitLines(o2[0]))
	p := Plan{State: "## Estado actual\n- todo archivado (12-nov · archivo)", Archive: []Archived{{File: 0, Lines: [2]int{h + 1, n}, Reason: ReasonSuperseded, Source: "work/x.md:1"}}}
	out, err := Assemble(AssembleInput{Unit: u2, Orig: o2, Plan: p, Date: "2026-11-13", Nonce: "other", S0: "def", SourceExists: exists})
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(u2, o2, out, "other"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Files["archivo/work/x/2026-11-13.md"], []byte("<!-- dreaming:n0nce f=0")) {
		t.Fatal("old markers were not archived literally")
	}
}
