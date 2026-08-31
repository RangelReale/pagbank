// Package edi consulta a API do Extrato EDI do PagBank
// (edi.api.pagbank.com.br/movement/v3.00).
//
// É a fonte completa do extrato de uma conta empresarial: além das transações,
// traz a movimentação financeira, os saques e os saldos. Em troca, exige
// ativação prévia do serviço (abrir chamado "Novas Ativações - EDI"), não tem
// sandbox, entrega os dados em D-1 e aceita uma única data por requisição — o
// Client percorre o período dia a dia.
package edi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/httpx"
	"github.com/RangelReale/pagbank/internal/sheet"
	"github.com/RangelReale/pagbank/internal/source"
)

const (
	// DefaultBaseURL é o ambiente de produção. O EDI não tem sandbox.
	DefaultBaseURL = "https://edi.api.pagbank.com.br"
	// DefaultVersion é a versão de layout em vigor.
	DefaultVersion = "v3.00"
	// DefaultPageSize é o teto de registros por página aceito pela API.
	DefaultPageSize = 1000

	// maxPages é um freio: sem o total de páginas na resposta, a paginação para
	// sozinha, mas um servidor que repita a mesma página faria um laço infinito.
	maxPages = 10000
)

// MovementTypes são os tipos de movimento que a API expõe, na ordem em que
// fazem sentido numa conferência.
var MovementTypes = []string{"transactional", "financial", "cashouts", "balances"}

// Descriptions explica cada tipo de movimento na ajuda da linha de comando.
var Descriptions = map[string]string{
	"transactional": "transações do dia (vendas, cancelamentos, chargebacks, ajustes)",
	"financial":     "movimentação financeira e liquidação dos recebíveis",
	"cashouts":      "saques e transferências para fora da conta",
	"balances":      "saldos da conta no dia",
}

// ValidateTypes recusa tipos de movimento que a API não conhece.
func ValidateTypes(types []string) error {
	for _, t := range types {
		if !slices.Contains(MovementTypes, t) {
			return fmt.Errorf("tipo de movimento %q não existe; use %s", t, strings.Join(MovementTypes, ", "))
		}
	}
	if len(types) == 0 {
		return errors.New("nenhum tipo de movimento selecionado")
	}
	return nil
}

// Client consulta o extrato EDI.
type Client struct {
	HTTP     *httpx.Client
	BaseURL  string
	Version  string
	User     string // número do estabelecimento
	Token    string
	PageSize int
	// Types são os movimentos a extrair; vazio significa todos.
	Types []string
	// Logf, quando definido, reporta o progresso (tipo, dia e página).
	Logf func(format string, args ...any)
}

// New monta o Client a partir das credenciais.
func New(c config.EDI, hc *httpx.Client) *Client {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		HTTP:     hc,
		BaseURL:  strings.TrimSuffix(base, "/"),
		Version:  DefaultVersion,
		User:     c.User,
		Token:    c.Token,
		PageSize: DefaultPageSize,
		Types:    slices.Clone(MovementTypes),
	}
}

// Name identifica a fonte nas mensagens ao usuário.
func (c *Client) Name() string { return "extrato EDI" }

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// authHeader monta o Basic com estabelecimento e token.
func (c *Client) authHeader() http.Header {
	cred := base64.StdEncoding.EncodeToString([]byte(c.User + ":" + c.Token))
	return http.Header{
		"Authorization": {"Basic " + cred},
		"Accept":        {"application/json"},
	}
}

// Fetch percorre cada tipo de movimento em cada dia do período, produzindo uma
// tabela por tipo.
func (c *Client) Fetch(ctx context.Context, p source.Period) (*source.Result, error) {
	res := &source.Result{}
	tipos := c.Types
	if len(tipos) == 0 {
		tipos = MovementTypes
	}

	var semDados, parciais []string
	for _, tipo := range tipos {
		b := sheet.NewBuilder(tipo)
		for _, dia := range p.Days() {
			estado, err := c.fetchDay(ctx, tipo, dia, b)
			if err != nil {
				return nil, err
			}
			switch estado {
			case diaAusente:
				semDados = append(semDados, fmt.Sprintf("%s/%s", tipo, dia.Format(source.DateLayout)))
			case diaParcial:
				parciais = append(parciais, fmt.Sprintf("%s/%s", tipo, dia.Format(source.DateLayout)))
			}
		}
		res.Tables = append(res.Tables, b.Build())
	}

	if len(parciais) > 0 {
		res.Warnf("o PagBank marcou como ainda em processamento (VALIDADO=FALSE): %s. Os dados de um dia só são garantidos em D+1 — reextraia amanhã para conferir",
			strings.Join(parciais, ", "))
	}
	if len(semDados) > 0 {
		res.Warnf("sem extrato disponível (HTTP 404) em: %s. Normalmente é dia sem movimento, mas também é o que a API responde para data anterior à ativação do EDI",
			strings.Join(semDados, ", "))
	}
	return res, nil
}

// estadoDia distingue os desfechos de um dia que não são erro.
type estadoDia int

const (
	diaOK estadoDia = iota
	diaAusente
	diaParcial
)

func (c *Client) fetchDay(ctx context.Context, tipo string, dia time.Time, b *sheet.Builder) (estadoDia, error) {
	data := dia.Format(source.DateLayout)
	estado := diaOK

	for page := 1; page <= maxPages; page++ {
		u := fmt.Sprintf("%s/movement/%s/%s/%s?pageNumber=%d&pageSize=%d",
			c.BaseURL, c.version(), tipo, data, page, c.pageSize())

		resp, err := c.HTTP.Get(ctx, u, c.authHeader())
		if err != nil {
			if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
				return diaAusente, nil
			}
			return estado, fmt.Errorf("consultando %s em %s (página %d): %w", tipo, data, page, explain(err))
		}

		// O header VALIDADO diz se o dia terminou de ser processado.
		if strings.EqualFold(strings.TrimSpace(resp.Header.Get("VALIDADO")), "FALSE") {
			estado = diaParcial
		}

		root, err := parse(resp.Body)
		if err != nil {
			return estado, fmt.Errorf("resposta JSON inesperada em %s/%s (página %d): %w", tipo, data, page, err)
		}
		regs, _ := records(root)
		c.logf("%s %s, página %d: %d registros", tipo, data, page, len(regs))

		for _, r := range regs {
			b.StartRow()
			// A data consultada entra como coluna: nos saldos ela é a única
			// referência temporal, e nas demais tabelas serve de rastro.
			b.Set("Data do movimento", sheet.KindDate, data)
			for _, f := range flatten(r) {
				b.Set(f.Path, f.Kind, f.Value)
			}
		}

		if fim(pagination(root), page, len(regs), c.pageSize()) {
			return estado, nil
		}
	}
	return estado, fmt.Errorf("%s em %s passou de %d páginas; interrompido para não entrar em laço", tipo, data, maxPages)
}

// fim decide se a paginação acabou. Com o total de páginas na resposta, ele
// manda; sem ele, uma página incompleta é o sinal de fim.
func fim(info pageInfo, page, lidos, pageSize int) bool {
	if info.Known {
		return page >= info.TotalPages
	}
	return lidos < pageSize
}

func (c *Client) version() string {
	if c.Version == "" {
		return DefaultVersion
	}
	return c.Version
}

func (c *Client) pageSize() int {
	if c.PageSize <= 0 || c.PageSize > DefaultPageSize {
		return DefaultPageSize
	}
	return c.PageSize
}

// explain traduz os erros HTTP mais comuns do EDI para algo acionável.
func explain(err error) error {
	se, ok := errors.AsType[*httpx.StatusError](err)
	if !ok {
		return err
	}
	switch se.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("credenciais recusadas (%d): confira %s (número do estabelecimento) e %s. O token do EDI é emitido por chamado e não é o token do painel",
			se.StatusCode, config.EnvEDIUser, config.EnvEDIToken)
	}
	return err
}
