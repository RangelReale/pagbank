// Package legacy consulta a API clássica de transações do PagSeguro
// (ws.pagseguro.uol.com.br/v2/transactions).
//
// É a fonte de acesso imediato: o token sai no painel do vendedor, sem chamado.
// Em troca ela só enxerga vendas — não a movimentação financeira completa da
// conta —, responde em XML, guarda apenas os últimos seis meses e aceita no
// máximo trinta dias por consulta. O Client cuida do fatiamento e da paginação;
// para o extrato completo, use o pacote edi.
package legacy

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/httpx"
	"github.com/RangelReale/pagbank/internal/sheet"
	"github.com/RangelReale/pagbank/internal/source"
)

const (
	// DefaultBaseURL é o ambiente de produção. Não há sandbox útil aqui.
	DefaultBaseURL = "https://ws.pagseguro.uol.com.br"

	// MaxWindowDays é o intervalo máximo aceito em uma consulta.
	MaxWindowDays = 30
	// MaxHistoryMonths é o quanto a API guarda de histórico.
	MaxHistoryMonths = 6
	// maxPageResults é o teto de registros por página.
	maxPageResults = 1000

	// TableName é o nome da tabela — e do arquivo — produzida por esta fonte.
	TableName = "transacoes"

	// clockSkew é a folga descontada de "agora" ao cortar o finalDate. O relógio
	// desta máquina pode estar alguns segundos à frente do relógio do PagBank, e
	// nesse caso a consulta inteira falharia por uma data "no futuro".
	clockSkew = time.Minute
)

// brasilia é o fuso em que a API interpreta as datas que recebe — ela não aceita
// deslocamento na query. O Brasil não tem mais horário de verão desde 2019,
// então o deslocamento é fixo e não depende de tzdata.
var brasilia = time.FixedZone("-03:00", -3*60*60)

// Client consulta as transações da conta.
type Client struct {
	HTTP    *httpx.Client
	BaseURL string
	Email   string
	Token   string
	// Logf, quando definido, reporta o progresso (janela e página).
	Logf func(format string, args ...any)
	// Now existe para o teste fixar "hoje" ao validar o limite de histórico.
	Now func() time.Time
}

// New monta o Client a partir das credenciais.
func New(c config.Legacy, hc *httpx.Client) *Client {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		HTTP:    hc,
		BaseURL: strings.TrimSuffix(base, "/"),
		Email:   c.Email,
		Token:   c.Token,
		Now:     time.Now,
	}
}

// Name identifica a fonte nas mensagens ao usuário.
func (c *Client) Name() string { return "transações (API legada)" }

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Fetch busca todas as transações do período, fatiando-o em janelas de trinta
// dias e percorrendo as páginas de cada janela.
func (c *Client) Fetch(ctx context.Context, p source.Period) (*source.Result, error) {
	res := &source.Result{}
	if err := c.checkHistory(p, res); err != nil {
		return nil, err
	}
	p, err := c.checkFuture(p, res)
	if err != nil {
		return nil, err
	}

	b := sheet.NewBuilder(TableName)
	vistos := make(map[string]bool)
	duplicadas := 0

	for _, w := range p.Windows(MaxWindowDays) {
		for page := 1; ; page++ {
			r, err := c.fetchPage(ctx, w, page)
			if err != nil {
				return nil, fmt.Errorf("consultando %s (página %d): %w", w, page, err)
			}
			c.logf("janela %s, página %d/%d: %d transações", w, page, max(r.TotalPages, 1), len(r.Transactions))

			for _, t := range r.Transactions {
				// A consulta é paginada sobre dados vivos: uma transação pode
				// reaparecer numa página seguinte se a ordenação mudar.
				if t.Code != "" && vistos[t.Code] {
					duplicadas++
					continue
				}
				vistos[t.Code] = true
				appendTransaction(b, t)
			}

			if page >= r.TotalPages || len(r.Transactions) == 0 {
				break
			}
		}
	}

	if duplicadas > 0 {
		res.Warnf("%d transação(ões) repetida(s) entre páginas foram descartadas pelo código", duplicadas)
	}
	res.Tables = []sheet.Table{b.Build()}
	return res, nil
}

// checkHistory avisa — ou recusa — quando o período pedido está fora do que a
// API guarda. Sem isso a resposta viria silenciosamente vazia.
func (c *Client) checkHistory(p source.Period, res *source.Result) error {
	limite := c.now().AddDate(0, -MaxHistoryMonths, 0)
	if p.To.Before(limite) {
		return fmt.Errorf("a API legada guarda só os últimos %d meses (desde %s) e o período pedido termina em %s; para dados mais antigos use o EDI",
			MaxHistoryMonths, limite.Format(source.DateLayout), p.To.Format(source.DateLayout))
	}
	if p.From.Before(limite) {
		res.Warnf("o período começa em %s, antes do limite de %d meses da API legada (%s): as transações anteriores a essa data não virão",
			p.From.Format(source.DateLayout), MaxHistoryMonths, limite.Format(source.DateLayout))
	}
	return nil
}

// checkFuture corta o período em hoje. A API recusa qualquer consulta que
// termine no futuro ("finalDate must be lower than allowed limit", código
// 13009), e como --to vale hoje por padrão, o caso comum cairia nessa recusa.
func (c *Client) checkFuture(p source.Period, res *source.Result) (source.Period, error) {
	hoje := c.hoje()
	if p.From.After(hoje) {
		return p, fmt.Errorf("o período começa em %s, no futuro: a API só responde até hoje (%s)",
			p.From.Format(source.DateLayout), hoje.Format(source.DateLayout))
	}
	if p.To.After(hoje) {
		res.Warnf("o período termina em %s, no futuro: a consulta foi cortada em hoje (%s)",
			p.To.Format(source.DateLayout), hoje.Format(source.DateLayout))
		p.To = hoje
	}
	return p, nil
}

// hoje é o dia corrente no fuso da API, na mesma forma que source.Period usa
// para as pontas do período: meia-noite UTC.
func (c *Client) hoje() time.Time {
	n := c.now().In(brasilia)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) fetchPage(ctx context.Context, w source.Period, page int) (*searchResult, error) {
	q := url.Values{}
	q.Set("email", c.Email)
	q.Set("token", c.Token)
	// O intervalo é fechado nas duas pontas; o fim do último dia é 23:59:59.
	q.Set("initialDate", w.From.Format("2006-01-02T15:04:05"))
	q.Set("finalDate", c.finalDate(w))
	q.Set("page", fmt.Sprint(page))
	q.Set("maxPageResults", fmt.Sprint(maxPageResults))

	u := c.BaseURL + "/v2/transactions?" + q.Encode()
	// O charset é obrigatório no Accept: o RESTEasy antigo que serve esta API
	// compara o media type com os parâmetros, e "application/xml" puro devolve
	// 406 Not Acceptable.
	resp, err := c.HTTP.Get(ctx, u, http.Header{"Accept": {"application/xml;charset=ISO-8859-1"}})
	if err != nil {
		return nil, explain(err)
	}

	var r searchResult
	if err := decodeXML(resp.Body, &r); err != nil {
		return nil, fmt.Errorf("resposta XML inesperada: %w", err)
	}
	return &r, nil
}

// finalDate é o fim da janela no formato da API. O fim do dia de hoje ainda é
// futuro, e a API recusa isso (código 13009), então o valor é cortado em agora.
func (c *Client) finalDate(w source.Period) string {
	fim := time.Date(w.To.Year(), w.To.Month(), w.To.Day(), 23, 59, 59, 0, brasilia)
	if agora := c.now().In(brasilia).Add(-clockSkew); fim.After(agora) {
		fim = agora
	}
	return fim.Format("2006-01-02T15:04:05")
}

// explain traduz os erros HTTP mais comuns desta API para algo acionável.
func explain(err error) error {
	se, ok := errors.AsType[*httpx.StatusError](err)
	if !ok {
		return err
	}
	if msgs := parseErrors(se.Body); len(msgs) > 0 {
		return fmt.Errorf("o PagBank recusou a consulta: %s", strings.Join(msgs, "; "))
	}
	switch se.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("credenciais recusadas (%d): confira %s e %s — o token da API não é a senha da conta",
			se.StatusCode, config.EnvEmail, config.EnvToken)
	case http.StatusNotFound:
		return fmt.Errorf("endpoint não encontrado (404): confira %s", config.EnvLegacyBaseURL)
	case http.StatusNotAcceptable:
		return fmt.Errorf("o PagBank recusou o formato pedido (406): esta API só responde ao Accept \"application/xml;charset=ISO-8859-1\"")
	}
	return err
}

// searchResult é a resposta de /v2/transactions.
type searchResult struct {
	XMLName           xml.Name      `xml:"transactionSearchResult"`
	Date              string        `xml:"date"`
	CurrentPage       int           `xml:"currentPage"`
	ResultsInThisPage int           `xml:"resultsInThisPage"`
	TotalPages        int           `xml:"totalPages"`
	Transactions      []transaction `xml:"transactions>transaction"`
}

type transaction struct {
	Date          string `xml:"date"`
	LastEventDate string `xml:"lastEventDate"`
	Code          string `xml:"code"`
	Reference     string `xml:"reference"`
	Type          string `xml:"type"`
	Status        string `xml:"status"`
	PaymentMethod struct {
		Type string `xml:"type"`
		Code string `xml:"code"`
	} `xml:"paymentMethod"`
	GrossAmount      string `xml:"grossAmount"`
	DiscountAmount   string `xml:"discountAmount"`
	FeeAmount        string `xml:"feeAmount"`
	NetAmount        string `xml:"netAmount"`
	ExtraAmount      string `xml:"extraAmount"`
	InstallmentCount string `xml:"installmentCount"`
	ItemCount        string `xml:"itemCount"`
}

// appendTransaction transcreve uma transação para a planilha. O código cru fica
// numa coluna própria ao lado da descrição: ele é o dado autoritativo, a
// descrição é conveniência (veja codigos.go).
func appendTransaction(b *sheet.Builder, t transaction) {
	b.StartRow()
	b.Set("Data", sheet.KindDateTime, normalizeTime(t.Date))
	b.Set("Última atualização", sheet.KindDateTime, normalizeTime(t.LastEventDate))
	b.Set("Código", sheet.KindText, t.Code)
	b.Set("Referência", sheet.KindText, t.Reference)
	b.Set("Tipo", sheet.KindText, describe(tipos, t.Type))
	b.Set("Tipo (código)", sheet.KindText, t.Type)
	b.Set("Status", sheet.KindText, describe(status, t.Status))
	b.Set("Status (código)", sheet.KindText, t.Status)
	b.Set("Meio de pagamento", sheet.KindText, describe(meiosDePagamento, t.PaymentMethod.Type))
	b.Set("Meio de pagamento (código)", sheet.KindText, t.PaymentMethod.Type)
	b.Set("Meio de pagamento (detalhe)", sheet.KindText, t.PaymentMethod.Code)
	b.Set("Valor bruto", sheet.KindNumber, t.GrossAmount)
	b.Set("Desconto", sheet.KindNumber, t.DiscountAmount)
	b.Set("Taxa", sheet.KindNumber, t.FeeAmount)
	b.Set("Valor líquido", sheet.KindNumber, t.NetAmount)
	b.Set("Valor extra", sheet.KindNumber, t.ExtraAmount)
	b.Set("Parcelas", sheet.KindNumber, t.InstallmentCount)
	b.Set("Itens", sheet.KindNumber, t.ItemCount)
}

// normalizeTime converte a data da API (RFC 3339 com milissegundos e fuso) para
// a forma canônica do pacote sheet, preservando o horário de Brasília que veio
// na resposta. O valor original é mantido se não for reconhecido.
func normalizeTime(v string) string {
	if v == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}

// apiError é o corpo de erro da API clássica.
type apiError struct {
	XMLName xml.Name `xml:"errors"`
	Errors  []struct {
		Code    string `xml:"code"`
		Message string `xml:"message"`
	} `xml:"error"`
}

func parseErrors(body string) []string {
	if !strings.Contains(body, "<errors") {
		return nil
	}
	var e apiError
	if err := decodeXML([]byte(body), &e); err != nil {
		return nil
	}
	msgs := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		msgs = append(msgs, fmt.Sprintf("%s (código %s)", item.Message, item.Code))
	}
	return msgs
}

// decodeXML interpreta a resposta. O CharsetReader é obrigatório: esta API
// declara ISO-8859-1 e o encoding/xml se recusa a decodificar um charset que
// não conhece.
func decodeXML(data []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = charsetReader
	return d.Decode(v)
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "latin1", "iso8859-1", "windows-1252":
		return &latin1Reader{r: input}, nil
	case "", "utf-8", "utf8":
		return input, nil
	}
	return nil, fmt.Errorf("charset %q não suportado", charset)
}

// latin1Reader converte ISO-8859-1 para UTF-8 byte a byte: em latin-1 cada byte
// é o próprio code point, então basta reencodá-lo.
type latin1Reader struct {
	r   io.Reader
	buf []byte // sobra de um rune que não coube na leitura anterior
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) && len(l.buf) > 0 {
		p[n] = l.buf[0]
		l.buf = l.buf[1:]
		n++
	}
	if n == len(p) {
		return n, nil
	}

	// Um byte latin-1 vira até dois bytes em UTF-8, então lê no máximo metade.
	src := make([]byte, max((len(p)-n)/2, 1))
	read, err := l.r.Read(src)
	for _, b := range src[:read] {
		var enc [2]byte
		size := encodeRune(enc[:], rune(b))
		for i := range size {
			if n < len(p) {
				p[n] = enc[i]
				n++
			} else {
				l.buf = append(l.buf, enc[i])
			}
		}
	}
	if n > 0 && err == io.EOF {
		return n, nil
	}
	return n, err
}

func encodeRune(dst []byte, r rune) int {
	if r < 0x80 {
		dst[0] = byte(r)
		return 1
	}
	dst[0] = 0xC0 | byte(r>>6)
	dst[1] = 0x80 | byte(r&0x3F)
	return 2
}
