package gendata

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"code.google.com/p/rog-go/parallel"

	"strings"

	"sourcegraph.com/sourcegraph/srclib/dep"
	"sourcegraph.com/sourcegraph/srclib/graph"
	"sourcegraph.com/sourcegraph/srclib/unit"
)

type SimpleRepoCmd struct {
	GenDataOpt

	NFiles []int `short:"f" long:"files" description:"number of files at each level" required:"yes"`
	NUnits []int `short:"u" long:"units" description:"number of units to generate; uses same input structure as --files" required:"yes"`
	NDefs  int   `long:"ndefs" description:"number of defs to generate per file" required:"yes"`
	NRefs  int   `long:"nrefs" description:"number of refs to generate per file" required:"yes"`
}

func (c *SimpleRepoCmd) Execute(args []string) error {
	if c.CommitID == "" && !c.GenSource {
		return fmt.Errorf("--commit must be non-empty or --gen-source must be true")
	}

	if err := removeGlob(".srclib-*"); err != nil {
		return err
	}

	units := make([]*unit.SourceUnit, 0)
	unitNames := hierarchicalNames("u", "unit", "", c.NUnits)
	for _, unitName := range unitNames {
		units = append(units, &unit.SourceUnit{
			Name:     fmt.Sprintf(unitName),
			Type:     "GoPackage",
			Repo:     c.Repo,
			CommitID: c.CommitID,
			Files:    []string{},
			Dir:      unitName,
		})
	}

	if c.GenSource {
		if err := removeGlob("unit_*"); err != nil {
			return err
		}
		if err := removeGlob("u_*"); err != nil {
			return err
		}
		if err := os.RemoveAll(".git"); err != nil {
			return err
		}
		if err := exec.Command("git", "init").Run(); err != nil {
			return err
		}

		// generate source files
		par := parallel.NewRun(runtime.GOMAXPROCS(0))
		for _, ut_ := range units {
			ut := ut_
			par.Do(func() error { return c.genUnit(ut) })
		}
		if err := par.Wait(); err != nil {
			return err
		}

		// get commit ID
		err := exec.Command("git", "add", "-A", ":/").Run()
		if err != nil {
			return err
		}
		err = exec.Command("git", "commit", "-m", "generated source").Run()
		if err != nil {
			return err
		}
		out, err := exec.Command("git", "log", "--pretty=oneline", "-n1").Output()
		if err != nil {
			return err
		}
		commitID := strings.Fields(string(out))[0]

		// update command to generate graph data
		c.CommitID = commitID
		c.GenSource = false
	}

	// generate graph data
	par := parallel.NewRun(runtime.GOMAXPROCS(0))
	for _, ut_ := range units {
		ut := ut_
		ut.CommitID = c.CommitID
		par.Do(func() error { return c.genUnit(ut) })
	}
	if err := par.Wait(); err != nil {
		return err
	}

	return nil
}

func (c *SimpleRepoCmd) genUnit(ut *unit.SourceUnit) error {
	defs := make([]*graph.Def, 0)
	refs := make([]*graph.Ref, 0)
	docs := make([]*graph.Doc, 0)

	for _, filename := range hierarchicalNames("dir", "file", ut.Name, c.NFiles) {
		ut.Files = append(ut.Files, filename)
		fileDefs, fileRefs, fileDocs, err := c.genFile(ut, filename)
		if err != nil {
			return err
		}
		defs, refs, docs = append(defs, fileDefs...), append(refs, fileRefs...), append(docs, fileDocs...)
	}

	if !c.GenSource {
		gr := graph.Output{Defs: defs, Refs: refs, Docs: docs}
		dp := make([]*dep.Resolution, 0)

		unitDir := filepath.Join(".srclib-cache", ut.CommitID, ut.Name)
		if err := os.MkdirAll(unitDir, 0700); err != nil {
			return err
		}

		unitFile, err := os.OpenFile(filepath.Join(unitDir, fmt.Sprintf("%s.unit.json", ut.Type)), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
		defer unitFile.Close()

		if err := json.NewEncoder(unitFile).Encode(ut); err != nil {
			return err
		}

		graphFile, err := os.OpenFile(filepath.Join(unitDir, fmt.Sprintf("%s.graph.json", ut.Type)), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
		defer graphFile.Close()

		if err := json.NewEncoder(graphFile).Encode(gr); err != nil {
			return err
		}

		depFile, err := os.OpenFile(filepath.Join(unitDir, fmt.Sprintf("%s.depresolve.json", ut.Type)), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
		defer depFile.Close()

		if err := json.NewEncoder(depFile).Encode(dp); err != nil {
			return err
		}
	}

	return nil
}

func (c *SimpleRepoCmd) genFile(ut *unit.SourceUnit, filename string) (defs []*graph.Def, refs []*graph.Ref, docs []*graph.Doc, err error) {
	offset := 0
	defName := "foo"
	docstring := "// this is a docstring"

	var sourceFile *os.File
	if c.GenSource {
		err := os.MkdirAll(filepath.Dir(filename), 0700)
		if err != nil {
			return nil, nil, nil, err
		}
		file, err := os.Create(filename)
		if err != nil {
			return nil, nil, nil, err
		}
		sourceFile = file
	}

	for i := 0; i < c.NDefs; i++ {
		def := &graph.Def{
			DefKey: graph.DefKey{
				Repo:     ut.Repo,
				CommitID: ut.CommitID,
				UnitType: ut.Type,
				Unit:     ut.Name,
				Path:     filepath.Join(filename, fmt.Sprintf("method_%d", i)),
			},
			Name:     defName,
			Exported: true,
			File:     filename,
			DefStart: uint32(offset),
			DefEnd:   uint32(offset + len(defName)),
		}
		if sourceFile != nil {
			_, err := sourceFile.WriteString(def.Name + " ")
			if err != nil {
				return nil, nil, nil, err
			}
		}
		offset += len(defName) + 1
		defs = append(defs, def)

		doc := &graph.Doc{
			DefKey: def.DefKey,
			Data:   docstring,
			File:   def.File,
			Start:  uint32(offset),
			End:    uint32(offset + len(docstring)),
		}
		if sourceFile != nil {
			_, err := sourceFile.WriteString(docstring + "\n")
			if err != nil {
				return nil, nil, nil, err
			}
		}
		offset += len(docstring) + 1
		docs = append(docs, doc)

		defRef := &graph.Ref{
			DefRepo:     def.Repo,
			DefUnitType: def.UnitType,
			DefUnit:     def.Unit,
			DefPath:     def.Path,
			Repo:        def.Repo,
			CommitID:    def.CommitID,
			UnitType:    def.UnitType,
			Unit:        def.Unit,
			Def:         true,
			File:        def.File,
			Start:       def.DefStart,
			End:         def.DefEnd,
		}
		refs = append(refs, defRef)
	}

	for i, defIdx := 0, 0; i < c.NRefs; i, defIdx = i+1, (defIdx+1)%c.NDefs {
		ref := &graph.Ref{
			DefRepo:     ut.Repo,
			DefUnitType: ut.Type,
			DefUnit:     ut.Name,
			DefPath:     filepath.Join(filename, fmt.Sprintf("method_%d", defIdx)),
			Repo:        ut.Repo,
			CommitID:    ut.CommitID,
			UnitType:    ut.Type,
			Unit:        ut.Name,
			Def:         false,
			File:        filename,
			Start:       uint32(offset),
			End:         uint32(offset + len(defName)),
		}
		refs = append(refs, ref)

		if sourceFile != nil {
			_, err := sourceFile.WriteString(defName + "\n")
			if err != nil {
				return nil, nil, nil, err
			}
		}

		offset += len(defName) + 1
	}

	// Close source file
	if sourceFile != nil {
		sourceFile.Close()
	}

	return defs, refs, docs, nil
}

func removeGlob(glob string) error {
	matches, err := filepath.Glob(glob)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.RemoveAll(match); err != nil {
			return err
		}
	}
	return nil
}
