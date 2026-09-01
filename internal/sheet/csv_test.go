package sheet

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "regrava os arquivos golden em testdata")

func exemplo() Table {
	return Table{
		Name: "transacoes",
		Columns: []Column{
			{Header: "Data", Kind: KindDate},
			{Header: "Data/hora", Kind: KindDateTime},
			{Header: "Código", Kind: KindText},
			{Header: "Descrição", Kind: KindText},
			{Header: "Valor bruto", Kind: KindNumber},
			{Header: "Valor líquido", Kind: KindNumber},
		},
		Rows: [][]string{
			{"2026-08-01", "2026-08-01T09:15:42", "REF-1", "Venda; à vista", "1234.56", "1198.12"},
			{"2026-08-02", "2026-08-02T18:00:00-03:00", "REF-2", "Estorno \"parcial\"", "-90.00", "-90.00"},
			// Linha curta e campos que o Excel trataria como fórmula ou número.
			{"", "", "=SOMA(A1)", "", "1000", ""},
		},
	}
}

func TestWriteGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, exemplo(), DefaultOptions()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	golden := filepath.Join("testdata", "transacoes.csv")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (rode: go test ./internal/sheet -update)", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("CSV divergiu do golden.\nobtido:\n%q\nesperado:\n%q", buf.String(), want)
	}
}

func TestWriteFormataParaExcelPtBR(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, exemplo(), DefaultOptions()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"\uFEFF",              // BOM, senão o Excel come os acentos
		"Data;Data/hora",      // separador ponto e vírgula
		"01/08/2026",          // data pt-BR
		"01/08/2026 09:15:42", // data/hora pt-BR
		"1234,56",             // vírgula decimal
		"-90,00",              // negativo preserva o sinal
		"\"Venda; à vista\"",  // campo com o separador vem entre aspas
		"'=SOMA(A1)",          // fórmula neutralizada
		"1000",                // inteiro sem parte decimal fica como está
		"\r\n",                // fim de linha Windows
	} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("saída não contém %q", want)
		}
	}
}

func TestWriteLinhaMenorQueAsColunas(t *testing.T) {
	tb := Table{
		Name:    "t",
		Columns: []Column{{Header: "a"}, {Header: "b"}, {Header: "c"}},
		Rows:    [][]string{{"1"}},
	}
	var buf bytes.Buffer
	if err := Write(&buf, tb, DefaultOptions()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if want := "1;;\r\n"; !bytes.Contains(buf.Bytes(), []byte(want)) {
		t.Errorf("saída = %q, quero conter %q", buf.String(), want)
	}
}

func TestDecimalComma(t *testing.T) {
	casos := []struct{ in, want string }{
		{"1234.56", "1234,56"},
		{"-0.01", "-0,01"},
		{"1000", "1000"},
		{"0.00", "0,00"},
		// Precisão preservada: passar por float64 arredondaria.
		{"12345678901234.99", "12345678901234,99"},
	}
	for _, c := range casos {
		got, ok := decimalComma(c.in)
		if !ok || got != c.want {
			t.Errorf("decimalComma(%q) = %q,%v, quero %q,true", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{"n/d", "1.2.3", "R$ 10,00", "-", "1e5"} {
		if got, ok := decimalComma(in); ok {
			t.Errorf("decimalComma(%q) = %q,true, quero ok=false", in, got)
		}
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteFile(dir, Table{Name: "mov: financeira", Columns: []Column{{Header: "a"}}}, DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got, want := filepath.Base(path), "mov__financeira.csv"; got != want {
		t.Errorf("nome = %q, quero %q", got, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("arquivo não foi criado: %v", err)
	}
}

func TestFileNameAtCarimbaDataEHora(t *testing.T) {
	quando := time.Date(2026, 9, 1, 14, 30, 22, 0, time.UTC)
	if got, want := FileNameAt("transacoes", quando), "transacoes-2026-09-01_143022.csv"; got != want {
		t.Errorf("FileNameAt = %q, quero %q", got, want)
	}
	// O carimbo entra antes da sanitização, então o nome sujo continua sendo limpo.
	if got, want := FileNameAt("mov: financeira", quando), "mov__financeira-2026-09-01_143022.csv"; got != want {
		t.Errorf("FileNameAt = %q, quero %q", got, want)
	}
}

func TestWriteFileAsUsaONomeDado(t *testing.T) {
	dir := t.TempDir()
	tab := Table{Name: "transacoes", Columns: []Column{{Header: "a"}}, Rows: [][]string{{"1"}}}

	// Duas gravações com carimbos diferentes não podem se sobrescrever: é para
	// isso que a interface web usa WriteFileAs em vez de WriteFile.
	var caminhos []string
	for _, quando := range []time.Time{
		time.Date(2026, 9, 1, 14, 30, 22, 0, time.UTC),
		time.Date(2026, 9, 1, 14, 31, 5, 0, time.UTC),
	} {
		path, err := WriteFileAs(dir, FileNameAt(tab.Name, quando), tab, DefaultOptions())
		if err != nil {
			t.Fatalf("WriteFileAs: %v", err)
		}
		caminhos = append(caminhos, path)
	}

	if got, want := filepath.Base(caminhos[0]), "transacoes-2026-09-01_143022.csv"; got != want {
		t.Errorf("nome = %q, quero %q", got, want)
	}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 2 {
		t.Errorf("%d arquivo(s) em %s, quero 2", len(entradas), dir)
	}
}
