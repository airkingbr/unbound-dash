# unbound-dash

Dashboard web para monitoramento e gerência de servidores [Unbound](https://nlnetlabs.nl/projects/unbound/about/)
recursivos rodando em Debian.

Binário único em Go (frontend embutido), sem dependências externas. Coleta
estatísticas via `unbound-control stats_noreset` e expõe uma área de
controle para os comandos mais usados do `unbound-control` (reload, flush
de cache, flush de zona, etc).

## Status

Esqueleto inicial:

- [x] API HTTP (login com sessão, `/api/stats`, `/api/status`, `/api/control/{cmd}`)
- [x] Frontend single-page (gráfico de QPS/cache hit, cards de estatísticas, painel de controle)
- [x] Cards de status mostram versão e uptime do Unbound; rodapé mostra a versão do unbound-dash em execução
- [x] Forward zones: encaminha a resolução de domínios específicos para outros resolvedores recursivos
- [x] Entradas estáticas: fixa um domínio em IPv4/IPv6 fixos
- [x] Instalador (`scripts/install.sh`) + unit systemd
- [x] Gerência de blocklist (`anatel-blocklist.conf`), com importação de ofícios em PDF
- [x] Log do Unbound ao vivo (streaming)
- [x] Testes DNS (consulta tipo dig, verificação de bloqueio, comparação de resolvedores, trace, reverso)
- [x] Benchmark do recursivo (cache frio/quente, lote, replay, carga/QPS, histograma, comparação)
- [ ] dnstap: estatísticas por IP/subnet, top domínios
- [ ] Histórico persistente (SQLite)
- [ ] Suporte a múltiplos servidores em uma única interface

## Instalação

Em um Debian que já roda Unbound:

```bash
curl -fsSL https://raw.githubusercontent.com/airkingbr/unbound-dash/main/scripts/install.sh | sudo bash
```

O instalador:

1. Baixa o binário da última release (defina `GITHUB_TOKEN` se o repositório for privado, ou use `-f BINARIO_LOCAL`)
2. Verifica se o `remote-control` do Unbound está habilitado; se não estiver, habilita (com backup do `unbound.conf`)
3. Gera `/etc/unbound-dash/config.json` (com senha de admin, gerada automaticamente se não informada via `-p`)
4. Instala e inicia o serviço systemd `unbound-dash`

Por padrão a interface fica disponível em `http://SEU_SERVIDOR:8080`.

O instalador também habilita `log-queries: yes` no Unbound (necessário para
as abas de Top consultas/clientes) e configura rotação do log
(`/etc/logrotate.d/unbound-dash`, por tamanho — `size 200M` — verificada a
cada hora via `/etc/cron.hourly/unbound-dash-logrotate`, não apenas uma vez
por dia), para evitar que o log cresça sem controle e encha o disco. Um
disco cheio impede o Unbound de regravar `/var/lib/unbound/root.key`
(trust anchor), o que quebra o `unbound-checkconf`/`unbound-control
status` com erro "failed to read /var/lib/unbound/root.key" — se isso
acontecer, libere espaço em disco e rode `unbound-anchor -a
/var/lib/unbound/root.key` para recriar o arquivo.

### Opções

```bash
curl -fsSL https://raw.githubusercontent.com/airkingbr/unbound-dash/main/scripts/install.sh | sudo bash -s -- -p "minha-senha" -l ":8080"
```

- `-v VERSION` — versão da release a baixar (padrão: `latest`)
- `-f BINARIO_LOCAL` — instala a partir de um binário local em vez de baixar uma release
- `-p SENHA` — senha de admin do dashboard (gerada automaticamente se omitida)
- `-l ENDERECO` — endereço/porta de escuta (padrão: `:8080`)

### Repositório privado

Se o repositório for privado, são necessários dois ajustes: o `curl` que
baixa o `install.sh` precisa de autenticação, e o `sudo` precisa repassar a
variável `GITHUB_TOKEN` para o script (o `sudo` não herda o ambiente por
padrão):

```bash
export GITHUB_TOKEN="ghp_xxxxxxxxxxxxxxxxxxxx"

curl -fsSL -H "Authorization: token ${GITHUB_TOKEN}" \
  -H "Accept: application/vnd.github.raw" \
  https://api.github.com/repos/airkingbr/unbound-dash/contents/scripts/install.sh \
  -o install.sh

sudo GITHUB_TOKEN="${GITHUB_TOKEN}" bash install.sh
```

O token precisa de permissão de leitura no repositório (`repo` para um PAT
clássico, ou `Contents: Read-only` para um fine-grained token).

## Atualização

O `install.sh` já instala `scripts/update.sh` em
`/usr/local/bin/update-unbound-dash`. Para atualizar para a última release,
basta rodar:

```bash
sudo update-unbound-dash
```

Ele baixa o novo binário, faz backup do binário atual
(`/usr/local/bin/unbound-dash.bak.<timestamp>`) e reinicia o serviço. Em
repositório privado, exporte `GITHUB_TOKEN` antes de rodar.

Caso queira baixar `update.sh` manualmente (ex.: em um servidor instalado
antes dessa versão), use:

```bash
curl -fsSL -o update.sh https://raw.githubusercontent.com/airkingbr/unbound-dash/main/scripts/update.sh
sudo bash update.sh
```

Em repositório privado, baixe via API com o token (mesmo esquema do
`install.sh`) e repasse `GITHUB_TOKEN` ao `sudo`:

```bash
curl -fsSL -H "Authorization: token ${GITHUB_TOKEN}" \
  -H "Accept: application/vnd.github.raw" \
  https://api.github.com/repos/airkingbr/unbound-dash/contents/scripts/update.sh \
  -o update.sh

sudo GITHUB_TOKEN="${GITHUB_TOKEN}" bash update.sh
```

Opções:

- `-v VERSION` — versão da release a instalar (padrão: `latest`)
- `-f BINARIO_LOCAL` — atualiza a partir de um binário local em vez de baixar uma release

## Desenvolvimento

```bash
go build ./...
go run ./cmd/unbound-dash -config ./config.dev.json
```

`config.json` de exemplo:

```json
{
  "listen_addr": ":8080",
  "unbound_control_bin": "/usr/sbin/unbound-control",
  "unbound_conf": "/etc/unbound/unbound.conf",
  "admin_password": "troque-isto"
}
```

## Comandos de controle suportados

`reload`, `status`, `dump_cache`, `flush_requestlist`, `flush_bogus`,
`flush_negative`, `flush <nome>`, `flush_zone <zona>`, `flush_type <nome> <tipo>`,
`flush_infra <ip|all>`, `verbosity <nivel>`.

## Bloqueios de domínios

A aba **Bloqueios** gerencia o arquivo `anatel-blocklist.conf` (lista de
`local-zone: ... always_nxdomain` incluída no `unbound.conf`):

- **Importar ofício (PDF)**: envie o PDF de um ofício de bloqueio judicial;
  o sistema extrai o texto com `pdftotext` e usa um *template* (pacote
  `internal/oficio`) para identificar a origem (ex: CyberGaeco/MPSP) e listar
  todos os domínios do anexo, junto com a referência (`fonte`) e a data do
  ofício. Os domínios extraídos podem ser revisados/editados antes de aplicar.
  Como cada órgão envia o ofício em um formato de tabela diferente, novos
  formatos exigem um novo template em `internal/oficio` (implementando a
  interface `Template`).
- **Adicionar bloqueio manual**: domínio + origem + fonte + data.
- **Lista de domínios bloqueados**: busca por domínio/origem/fonte, paginação
  de 100 em 100, remoção individual e **remoção em lote** (seleciona vários
  e remove de uma vez, com um único `flush_zone .` no final).

Toda alteração na blocklist recarrega o Unbound (`reload`) e limpa o cache
(`flush_zone`) para que o bloqueio/desbloqueio tenha efeito imediato.

## Forward zones

A aba **Forward Zones** gerencia o arquivo `forwardzone.conf` (incluído no
`unbound.conf`), que encaminha a resolução de domínios específicos para
outro(s) resolvedor(es) recursivo(s) em vez do caminho normal via raiz —
útil quando um domínio específico tem problemas de resolução iterativa
(ex: lentidão ou falha intermitente resolvendo direto pela raiz).

Cada entrada tem um domínio e uma lista de IPs (`forward-addr`), gerando
um bloco no formato nativo do Unbound:

```
forward-zone:
    name: "exemplo.com.br."
    forward-addr: 1.1.1.1
    forward-addr: 8.8.8.8
```

Toda alteração recarrega o Unbound (`reload`) e limpa o cache da zona
afetada (`flush_zone`) para que o encaminhamento tenha efeito imediato.

Em servidores que já tinham o unbound-dash instalado antes desse recurso,
basta rodar `sudo update-unbound-dash` — o `update.sh` cria o
`forwardzone.conf`, adiciona o include no `unbound.conf` e recarrega o
Unbound automaticamente.

## Entradas estáticas

A aba **Entradas Estaticas** gerencia o arquivo `staticentries.conf`
(incluído no `unbound.conf`), que fixa um domínio em um IPv4 e/ou IPv6 fixo
em vez de resolvê-lo normalmente — útil para manter um domínio funcionando
mesmo que o DNS real dele mude ou fique inacessível.

Ao adicionar, escolha o host e o tipo (IPv4, IPv6 ou ambos), gerando um
bloco no formato nativo do Unbound:

```
local-zone: "exemplo.com." static
local-data: "exemplo.com. IN A 203.0.113.10"
local-data: "exemplo.com. IN AAAA 2001:db8::10"
```

Toda alteração recarrega o Unbound (`reload`) e limpa o cache da zona
afetada (`flush_zone`). Em servidores que já tinham o unbound-dash
instalado antes desse recurso, `sudo update-unbound-dash` cria o
`staticentries.conf` e adiciona o include automaticamente.

## Testes DNS

A aba **Testes DNS** oferece ferramentas de diagnóstico estilo `dig`,
implementadas em Go puro (`internal/dnstools`, sem depender de binários
externos):

- **Consulta DNS (dig)**: resolve um nome com o tipo de registro escolhido
  (A, AAAA, CNAME, MX, TXT, NS, SOA, CAA, SRV, ANY) contra o resolvedor local
  (`127.0.0.1:53`) ou outro servidor informado. Mostra status (NOERROR,
  NXDOMAIN, SERVFAIL...), tempo de resposta, flag DNSSEC (`AD`) e os
  registros de resposta/autoridade.
- **Verificar bloqueio**: informa se um domínio está na blocklist (direto ou
  via zona pai bloqueada) e confirma com uma consulta real ao resolvedor
  local — útil para validar se um bloqueio está realmente em vigor.
- **Comparar resolvedores**: roda a mesma consulta em paralelo contra o
  Unbound local, Cloudflare (`1.1.1.1`) e Google (`8.8.8.8`), lado a lado —
  ajuda a identificar se um problema é do seu recursivo ou de origem/rede.
- **Rastrear resolução (`dig +trace`)**: faz a resolução iterativa a partir
  dos servidores raiz, mostrando cada salto de delegação (zona, servidor,
  status, tempo e registros retornados).
- **Lookup reverso (PTR)**: resolve hostname a partir de um IP.

## Benchmark do recursivo

A aba **Benchmark** ajuda a avaliar o desempenho do Unbound como recursivo
(`internal/benchmark`):

- **Cache: frio vs quente**: para cada domínio, executa `flush_zone`, mede o
  tempo da 1ª consulta (resolução recursiva completa) e da 2ª (servida do
  cache), mostrando o ganho (`speedup`).
- **Lote de domínios**: consulta uma lista de domínios (uma lista padrão com
  domínios populares globais e brasileiros, ou uma lista informada) e mostra
  o tempo de cada um além de estatísticas agregadas (mín/média/p50/p95/máx).
- **Replay do top domínios**: reconsulta os domínios mais requisitados
  recentemente (a partir do log de consultas do Unbound), simulando a carga
  real do ambiente.
- **Teste de carga (QPS)**: dispara N consultas concorrentes para um domínio
  e mede a vazão (consultas/segundo) e a distribuição de latência.
- **Histograma de tempos de resposta**: exibe o histograma interno do
  Unbound (`unbound-control stats_noreset`), sem gerar tráfego adicional.
- **Comparar com resolvedores públicos**: roda o teste em lote contra o
  Unbound local, Cloudflare e Google, e compara as estatísticas agregadas —
  mostra se o recursivo local está mais rápido que usar um resolvedor
  público diretamente.

Essas ferramentas não exigem nenhuma dependência adicional além do que já é
instalado pelo `scripts/install.sh`.
