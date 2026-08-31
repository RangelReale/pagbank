// Package source define o contrato comum das fontes de dados do PagBank e os
// utilitários de período que elas compartilham.
package source

import (
	"context"
	"fmt"
	"time"

	"github.com/RangelReale/pagbank/internal/sheet"
)

// Result é o que uma fonte devolve: uma tabela por tipo de movimento, mais os
// avisos que o usuário precisa ver antes de confiar na planilha.
type Result struct {
	Tables   []sheet.Table
	Warnings []string
}

// Records é o total de linhas em todas as tabelas.
func (r *Result) Records() int {
	n := 0
	for _, t := range r.Tables {
		n += len(t.Rows)
	}
	return n
}

// Warnf acrescenta um aviso ao resultado.
func (r *Result) Warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Source é uma origem de movimentação da conta.
type Source interface {
	// Name identifica a fonte nas mensagens ao usuário.
	Name() string
	// Fetch busca a movimentação do período, inclusive nas duas pontas.
	Fetch(ctx context.Context, p Period) (*Result, error)
}

// DateLayout é o formato aceito nas flags --from e --to.
const DateLayout = "2006-01-02"

// Period é um intervalo de dias fechado nas duas pontas.
type Period struct {
	From time.Time
	To   time.Time
}

// ParseDate lê uma data AAAA-MM-DD à meia-noite UTC. As duas APIs trabalham em
// dias de calendário, então não há hora nem fuso a preservar aqui.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("data %q inválida, use o formato AAAA-MM-DD", s)
	}
	return t, nil
}

// NewPeriod monta um período validado.
func NewPeriod(from, to time.Time) (Period, error) {
	p := Period{From: truncateDay(from), To: truncateDay(to)}
	if p.To.Before(p.From) {
		return Period{}, fmt.Errorf("--to (%s) é anterior a --from (%s)", p.To.Format(DateLayout), p.From.Format(DateLayout))
	}
	return p, nil
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Days lista todos os dias do período. O EDI aceita uma data por requisição, e é
// esse laço que a extração percorre.
func (p Period) Days() []time.Time {
	var days []time.Time
	for d := p.From; !d.After(p.To); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	return days
}

// Len é a quantidade de dias do período.
func (p Period) Len() int {
	return int(p.To.Sub(p.From)/(24*time.Hour)) + 1
}

// Windows fatia o período em pedaços de no máximo maxDays dias, para APIs que
// limitam o tamanho do intervalo consultável de uma vez.
func (p Period) Windows(maxDays int) []Period {
	if maxDays < 1 {
		maxDays = 1
	}
	var out []Period
	for start := p.From; !start.After(p.To); start = start.AddDate(0, 0, maxDays) {
		end := start.AddDate(0, 0, maxDays-1)
		if end.After(p.To) {
			end = p.To
		}
		out = append(out, Period{From: start, To: end})
	}
	return out
}

// String descreve o período do jeito que aparece nas mensagens.
func (p Period) String() string {
	if p.From.Equal(p.To) {
		return p.From.Format(DateLayout)
	}
	return p.From.Format(DateLayout) + " a " + p.To.Format(DateLayout)
}
