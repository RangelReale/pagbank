package web

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// fluxo transmite eventos para a página enquanto a requisição continua aberta,
// no formato Server-Sent Events.
//
// SSE é o que a biblioteca padrão dá de graça: o servidor só precisa de um
// http.Flusher e o navegador de um EventSource. Um WebSocket exigiria handshake
// e enquadramento escritos à mão — o go.mod não tem dependências e não vai ter —
// e uma consulta periódica exigiria um registro de tarefas com estado no
// servidor, que é justamente o que este desenho evita: a extração roda no
// goroutine do próprio handler.
type fluxo struct {
	w  http.ResponseWriter
	fl http.Flusher
}

// novoFluxo prepara a resposta para o streaming. Devolve ok=false quando o
// ResponseWriter não sabe descarregar o buffer, caso em que nada do que fosse
// escrito chegaria à página antes do fim da requisição.
func novoFluxo(w http.ResponseWriter) (*fluxo, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// Sem isto um proxy no meio do caminho pode segurar os eventos até o fim.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return &fluxo{w: w, fl: fl}, true
}

// enviar serializa dados em JSON e os manda como um evento nomeado. O JSON
// resolve sozinho a única regra chata do formato — o campo data não pode conter
// quebra de linha crua —, porque escapa as que houver no texto.
func (f *fluxo) enviar(evento string, dados any) error {
	b, err := json.Marshal(dados)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f.w, "event: %s\ndata: %s\n\n", evento, b); err != nil {
		return err
	}
	f.fl.Flush()
	return nil
}

// bater manda um comentário SSE, que a página ignora. Serve para o batimento:
// enquanto a escrita não falha, a aba continua aberta do outro lado.
func (f *fluxo) bater() error {
	if _, err := fmt.Fprint(f.w, ": batimento\n\n"); err != nil {
		return err
	}
	f.fl.Flush()
	return nil
}

// eventoProgresso é uma linha do Logf da fonte, já redigida. Feitos e Total vêm
// do gancho ProgressoDetalhe e valem zero enquanto a etapa não tem total
// conhecido — a busca por data só descobre o número de páginas ao receber a
// primeira resposta, e aí a barra fica indeterminada.
type eventoProgresso struct {
	Texto  string `json:"texto"`
	Feitos int    `json:"feitos"`
	Total  int    `json:"total"`
}

// eventoErro é o que interrompe a extração. Aviso não vem por aqui: ele viaja
// no eventoFim, porque apesar dele a planilha foi gerada.
//
// Tipo separa o cancelamento pedido pelo usuário do resto, para a página não
// pintar de vermelho o que ela mesma provocou.
type eventoErro struct {
	Tipo     string `json:"tipo"` // "cancelado" ou "outro"
	Mensagem string `json:"mensagem"`
}

// arquivoGerado é um CSV que a extração deixou no disco.
type arquivoGerado struct {
	Nome    string `json:"nome"`
	Caminho string `json:"caminho"`
	Linhas  int    `json:"linhas"`
}

// eventoFim é o relatório da extração que terminou.
type eventoFim struct {
	Arquivos []arquivoGerado `json:"arquivos"`
	Pasta    string          `json:"pasta"`
	Linhas   int             `json:"linhas"`
	Avisos   []string        `json:"avisos"`
	Duracao  string          `json:"duracao"`
	Vazio    bool            `json:"vazio"`
}
