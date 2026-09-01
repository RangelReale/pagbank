package legacy

import "fmt"

// As tabelas abaixo traduzem os códigos numéricos da API clássica para texto,
// só como conveniência de leitura da planilha. Em toda linha o código cru
// também é gravado, numa coluna própria — é ele o dado autoritativo, e é por
// ele que se deve filtrar ou conferir.
//
// Um código que não estiver aqui vira "código N" em vez de célula vazia, para
// que a ausência de tradução nunca se confunda com ausência de dado.
//
// Para acrescentar ou corrigir uma tradução, edite o mapa correspondente: essa
// é a única mudança necessária.

// status são os valores de <status>.
var status = map[string]string{
	"1": "Aguardando pagamento",
	"2": "Em análise",
	"3": "Paga",
	"4": "Disponível",
	"5": "Em disputa",
	"6": "Devolvida",
	"7": "Cancelada",
	"8": "Debitado",
	"9": "Retenção temporária",
}

// meiosDePagamento são os valores de <paymentMethod><type>. Os códigos 8 e 11
// não constam da documentação pública: apareceram em 12 das 21 vendas da
// primeira extração real (01/09/2026) e foram confirmados pelo titular da
// conta, com a taxa cobrada de reforço — 1,10% no 8, 0,50% cravado no 11.
var meiosDePagamento = map[string]string{
	"1":  "Cartão de crédito",
	"2":  "Boleto",
	"3":  "Débito online (TEF)",
	"4":  "Saldo PagBank",
	"5":  "Oi Paggo",
	"7":  "Depósito em conta",
	"8":  "Cartão de débito",
	"11": "PIX",
}

// tipos são os valores de <transaction><type>.
//
// Quase vazio de propósito: a documentação pública do PagBank não traz mais a
// tabela desse campo, e as listas que circulam em SDKs de terceiros divergem
// entre si. Preencher com um palpite marcaria linhas do extrato com a descrição
// errada, o que é pior do que deixá-las com o código. Só entra aqui o que for
// confirmado numa conta real.
//
// O código 1 é o das vendas comuns: foi o tipo de todas as 21 transações da
// primeira extração real (01/09/2026), em crédito, débito e PIX, pagas,
// disponíveis e canceladas.
var tipos = map[string]string{
	"1": "Pagamento",
}

// describe traduz um código, caindo para "código N" quando não conhece o valor.
func describe(tabela map[string]string, codigo string) string {
	if codigo == "" {
		return ""
	}
	if d, ok := tabela[codigo]; ok {
		return d
	}
	return fmt.Sprintf("código %s", codigo)
}
