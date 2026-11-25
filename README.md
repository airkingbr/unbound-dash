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
- [x] Blocklist DNS sincronizada com a API do ISP Tools (ofícios Anatel, Operação 404, decisões judiciais), aplicada sem reload
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

O instalador faz as perguntas logo no início e depois segue sozinho:

1. **Senha de admin do painel**: digite uma ou deixe em branco para gerar uma aleatória.
2. **Blocklist DNS (ISP Tools)**: pergunta se você tem um token da API.
   - Se tiver, o token é validado na hora (`/versao`). Ele avisa se o token é
     inválido/revogado (401), se o IP do servidor não está liberado (403,
     mostrando o IP a liberar) ou se não há acesso à API.
   - Se **não** tiver, a instalação continua normalmente, só que **sem aplicar
     bloqueios**. Para ativar depois, coloque o token em `"isptools_token"` no
     `/etc/unbound-dash/config.json` e rode `systemctl restart unbound-dash`.
3. Baixa o binário da última release (ou usa `-f BINARIO_LOCAL`).
4. Verifica se o `remote-control` do Unbound está habilitado; se não estiver, habilita (com backup do `unbound.conf`).
5. Gera `/etc/unbound-dash/config.json` (permissão 600), instala e (re)inicia o serviço systemd `unbound-dash`.
6. Com token, espera a primeira sincronização da blocklist e mostra um resumo
   com o endereço do painel, a senha gerada e a situação da blocklist.

Por padrão a interface fica disponível em `http://SEU_SERVIDOR:8080`.

Instalação não interativa (sem terminal), já com o token:

```bash
curl -fsSL https://raw.githubusercontent.com/airkingbr/unbound-dash/main/scripts/install.sh | sudo ISPTOOLS_TOKEN="isptbl_..." bash
```

Use `ISPTOOLS_TOKEN=nenhum` para instalar sem blocklist sem que ele pergunte.
Rodar o instalador de novo num servidor já instalado mantém a senha e o token
configurados.

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
(`/usr/local/bin/unbound-dash.bak.<timestamp>`, mantendo só os 3 mais
recentes) e reinicia o serviço. Em
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
  "isptools_token": "isptbl_...",
  "admin_password": "troque-isto"
}
```

Testes: `go test ./...`

## Comandos de controle suportados

`reload`, `status`, `dump_cache`, `flush_requestlist`, `flush_bogus`,
`flush_negative`, `flush <nome>`, `flush_zone <zona>`, `flush_type <nome> <tipo>`,
`flush_infra <ip|all>`, `verbosity <nivel>`.

## Bloqueios de domínios (Blocklist DNS do ISP Tools)

A lista de domínios bloqueados (ofícios Anatel, Operação 404, decisões
judiciais — ~160 mil domínios) vem da API **Blocklist DNS do ISP Tools**
(`https://isptools.gbti.net.br/api/v1/blocklist`). O unbound-dash a mantém
sincronizada sozinho; não há mais cadastro manual nem importação de ofícios
em PDF. Exceções (desbloqueios) são cadastradas no ISP Tools, por empresa ou
por token, e já chegam aplicadas na lista.

### Configuração

1. No ISP Tools, gere um token em *Blocklist DNS → Clientes e tokens* (um
   token por cliente/servidor; o token é mostrado uma única vez). Se o token
   for restrito por IP, libere o IP público de saída do servidor
   (`curl -s https://isptools.gbti.net.br/api/v1/blocklist/ip`).
2. Coloque o token em `/etc/unbound-dash/config.json` (arquivo com
   permissão 600; o token nunca é exibido na interface nem em log):

   ```json
   "isptools_token": "isptbl_..."
   ```

3. `systemctl restart unbound-dash`. A primeira sincronização roda ~15 s
   depois do início.

Opções (todas opcionais): `isptools_sync_minutes` (padrão `60`),
`isptools_blocklist_file` (padrão
`/etc/unbound/unbound.conf.d/isptools-blocklist.conf`), `isptools_api_url`,
`state_dir` (padrão `/var/lib/unbound-dash`), `unbound_checkconf_bin`.

### Como funciona

A cada hora (mais alguns minutos aleatórios por instalação):

1. Consulta `/versao`; se o md5 da lista deste token não mudou, para aí.
2. Baixa `/lista?formato=texto` (gzip), confere o md5 e recusa listas com
   menos de 1000 domínios.
3. Grava o arquivo persistente `isptools-blocklist.conf` (garante o
   `include:` no `unbound.conf`) e valida com `unbound-checkconf`; se
   reprovar, desfaz tudo.
4. Aplica **só as diferenças** no Unbound em execução, com
   `unbound-control local_zones` / `local_zones_remove` — **sem `reload`**,
   então o cache é preservado e o bloqueio vale na hora.
5. Registra versão, total e +adicionados/−removidos no histórico.

Se qualquer etapa falhar (rede, 5xx, md5 diferente, lista pequena demais,
`checkconf`), a lista atual é mantida. `401` (token inválido/revogado) pausa
a sincronização automática; `403` mostra na tela o IP a ser liberado; `429`
respeita o `Retry-After`.

Domínios que têm uma **entrada estática** no dashboard não são tocados pela
blocklist (a entrada estática prevalece).

Não rode o script oficial `isptools-blocklist` no mesmo Unbound: os dois
gerenciariam as mesmas zonas.

### Aba Bloqueios

Situação (em dia / desatualizado / erro), total de domínios, versão e data
da lista, histórico de sincronizações, botão **Sincronizar agora**, busca na
lista aplicada e **Testar bloqueio** (consulta o Unbound local e confirma o
NXDOMAIN).

### Migração do antigo `anatel-blocklist.conf`

Em servidores que usavam a blocklist manual, a primeira sincronização
bem-sucedida com a API desativa o arquivo antigo automaticamente: comenta o
`include:` dele no `unbound.conf` (com backup), renomeia o arquivo para
`anatel-blocklist.conf.migrado-<timestamp>` e remove do Unbound em execução
os domínios antigos que não estão na lista da API. Até lá, o arquivo antigo
continua valendo.

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
- **Verificar bloqueio**: informa se um domínio está na blocklist do ISP Tools (direto ou
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
