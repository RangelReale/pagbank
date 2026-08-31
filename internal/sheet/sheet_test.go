package sheet

import (
	"reflect"
	"testing"
)

func TestBuilderPreencheColunasQueSurgemDepois(t *testing.T) {
	// Cenário do EDI: um campo só aparece num registro da segunda página.
	b := NewBuilder("transacional")

	b.StartRow()
	b.Set("codigo", KindText, "ABC1")
	b.Set("valor", KindNumber, "10.50")

	b.StartRow()
	b.Set("codigo", KindText, "ABC2")
	b.Set("valor", KindNumber, "-3.00")
	b.Set("nsu", KindText, "998877")

	got := b.Build()

	want := Table{
		Name: "transacional",
		Columns: []Column{
			{Header: "codigo", Kind: KindText},
			{Header: "valor", Kind: KindNumber},
			{Header: "nsu", Kind: KindText},
		},
		Rows: [][]string{
			{"ABC1", "10.50", ""},
			{"ABC2", "-3.00", "998877"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() = %+v, quero %+v", got, want)
	}
}

func TestColumnRebaixaParaTextoQuandoOTipoConflita(t *testing.T) {
	b := NewBuilder("t")
	b.StartRow()
	b.Set("campo", KindNumber, "1.00")
	b.StartRow()
	// A mesma chave chegou como texto: manter KindNumber faria o writer tentar
	// formatar "n/d" como número.
	b.Set("campo", KindText, "n/d")

	got := b.Build()
	if got.Columns[0].Kind != KindText {
		t.Errorf("Kind = %v, quero text", got.Columns[0].Kind)
	}
	if len(got.Columns) != 1 {
		t.Errorf("colunas = %d, quero 1", len(got.Columns))
	}
}

func TestBuilderLenContaALinhaAberta(t *testing.T) {
	b := NewBuilder("t")
	if b.Len() != 0 {
		t.Fatalf("Len() = %d, quero 0", b.Len())
	}
	b.StartRow()
	b.Set("a", KindText, "x")
	if b.Len() != 1 {
		t.Errorf("Len() = %d, quero 1", b.Len())
	}
	if got := b.Build(); len(got.Rows) != 1 {
		t.Errorf("linhas = %d, quero 1", len(got.Rows))
	}
}

func TestBuilderVazioProduzTabelaVazia(t *testing.T) {
	got := NewBuilder("t").Build()
	if !got.Empty() {
		t.Errorf("Empty() = false, quero true")
	}
	if got.Rows == nil {
		t.Errorf("Rows = nil, quero slice vazio (o writer itera sobre ele)")
	}
}

func TestSetSemStartRowEntraEmPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("Set sem StartRow deveria entrar em panic")
		}
	}()
	NewBuilder("t").Set("a", KindText, "x")
}
