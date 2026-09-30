# SDK 사용 가이드

[README](README.ko.md) | [English](guide.md) | 한국어

아래 코드는 개별 API의 사용 예시입니다. 전체 실행 코드는 [예제](../examples/README.ko.md)를 참고하세요.


예제는 별도 표시가 없으면 `acp1` 기준입니다. `MyAgent`, `MyClient`, `ctx` 등의 값은 애플리케이션에서 준비합니다.

- [아키텍처](#아키텍처)
- [에이전트와 클라이언트 구현](#에이전트와-클라이언트-구현)
- [세션과 업데이트](#세션과-업데이트)
- [연결과 버전](#연결과-버전)
- [에러와 미들웨어](#에러와-미들웨어)
- [데이터와 확장](#데이터와-확장)

## 아키텍처

| 구성 요소 | 역할 |
| --- | --- |
| `acp` (루트) | `Option`, `Transport`와 stdio transport, `Middleware`, `RequestError`, `SessionStore` (`MemoryStore`, `FileStore`), `TurnTracker`, 타입 있는 확장(`CallExt`, `ExtRouter`, `ExtMethodHandler`) |
| `acphttp` | 초안 RFD를 따르는 Streamable HTTP·WebSocket transport. stdio 프로그램이 링크하지 않도록 루트와 분리 |
| `acp1.AgentSideConnection` | `Agent`를 제공하고 상대편 클라이언트를 호출 |
| `acp1.ClientSideConnection` | `Client`를 제공하고 상대편 에이전트를 호출 |
| `acp1.SpawnAgent`, `acp1.Pipe` | 자식 프로세스 에이전트, 또는 메모리 내 양쪽 연결 |
| `acp1.ClientSession`, `acp1.Turn` | 세션에 프롬프트를 보내고 그 턴의 업데이트를 읽음 |
| `acp1.SessionManager` | 스토어 기반 세션 수명주기와 턴 취소 |
| `acp1.SessionStream` | union을 직접 만들지 않고 세션 업데이트 전송 |
| `acp1.TerminalHandle` | 터미널 ID와 세션 ID를 묶은 핸들 |
| `acp1.CapabilitiesOf` | 에이전트가 구현한 인터페이스에서 capability 유도 |
| `acp2` | 초안 ACP v2(`schema/v2`)용 동일 구조의 파사드 |
| `router.ProtocolRouter` | v1·v2 에이전트를 한 엔드포인트로 서비스, **`router.ClientConnector`**: 클라이언트 쪽 v2 우선 연결과 v1 fallback |
| `acpmcp` (별도 모듈, 불안정) | MCP Go SDK 기반 MCP-over-ACP. `HostV1`/`HostV2`가 클라이언트의 MCP 서버를 제공하고 `DialerV1`/`DialerV2`가 에이전트를 연결 |
| `schema/v1`, `schema/v2` | 생성된 와이어 타입, union, Zod 검증 |

## 에이전트와 클라이언트 구현

### 에이전트 구현

```go
conn := acp1.NewAgentSideConnection(func(c *acp1.AgentSideConnection) acp1.Agent {
    return &MyAgent{client: c} // 연결 자체가 상대편 Client 입니다
}, acp.NewStdioTransport(os.Stdin, os.Stdout))

if err := conn.Start(context.Background()); err != nil {
    log.Fatal(err)
}
```

`Agent` 인터페이스에 반드시 필요한 메서드는 `Initialize`, `NewSession`, `Prompt`, `CancelSession`
네 개뿐입니다. 나머지는 선택적 인터페이스(`acp1.Authenticator`, `acp1.SessionLoader`,
`acp1.SessionLister`, `acp1.SessionModeSetter`, `acp1.NesHandler` 등)로 구현합니다.
`acp1.CapabilitiesOf(agent)`가 구현된 인터페이스에서 capability를 유도해 주므로, `Initialize`
응답이 연결이 거부할 메서드를 광고하는 일이 없습니다:

```go
caps := acp1.CapabilitiesOf(a)
caps.PromptCapabilities = &schema.PromptCapabilities{Image: new(true)} // 콘텐츠 capability는 직접 설정
return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion, AgentCapabilities: caps}, nil
```

`Initialize`에 응답한 뒤에는 `conn.ClientCapabilities()`가 클라이언트가 알린 capability를 돌려주므로
에이전트가 따로 보관할 필요가 없습니다. v1 `SessionStream`의 `ReadTextFile`, `WriteTextFile`,
`NewTerminal`은 이를 확인해, 클라이언트가 해당 capability를 알리지 않았으면 요청을 보내지 않고
`errors.ErrUnsupported`에 해당하는 오류로 실패합니다.

```go
if a.conn.ClientCapabilities().GetTerminal() {
    // 에디터 터미널에서 명령 실행
}
```

### 클라이언트 구현

```go
client := &MyClient{}
agent, err := acp1.SpawnAgent(ctx, exec.Command("my-agent"), func(*acp1.ClientSideConnection) acp1.Client {
    return client
})
if err != nil {
    log.Fatal(err)
}
defer agent.Close()

if _, err := agent.Initialize(ctx, &acp1.InitializeRequest{ // ProtocolVersion이 0이면 acp1.ProtocolVersion을 보냄
    ClientCapabilities: acp1.ClientCapabilitiesOf(client),
}); err != nil {
    log.Fatal(err)
}
session, err := agent.StartSession(ctx, &acp1.NewSessionRequest{Cwd: cwd})
if err != nil {
    log.Fatal(err)
}

turn, err := session.Prompt(ctx, acp1.TextBlock("README.md를 요약해줘"))
if err != nil {
    log.Fatal(err)
}
for update := range turn.Updates() {
    render(update) // 도구 호출, 계획, 메시지 조각...
}
response, err := turn.Wait() // 또는: text, response, err := turn.Text()
if err != nil {
    log.Fatal(err)
}
```

`SpawnAgent`는 읽기 루프를 이미 시작한 상태로 돌려주며, `agent.Wait()`가 프로세스와 연결이 어떻게
끝났는지 알려 줍니다. `cmd.Stderr`를 지정하지 않으면 에이전트의 stderr는 부모 프로세스로 전달됩니다.
`acp1.Pipe`는 에이전트와 클라이언트를 메모리에서 연결하므로 테스트에 유용합니다. `acp1test.Connect`는
이를 테스트용으로 감싸고, `acp1test.Client`는 에이전트가 보낸 업데이트와 권한 요청을 기록하며
(`Updates`, `Text`, `WaitFor`) 요청에는 `acp1test.AllowOnce`, `AllowAlways`, `Reject` 또는 직접 만든 함수로
응답합니다. v2는 `acp2test`가 같은 역할을 합니다.

`Client` 인터페이스에 필요한 메서드는 `SessionUpdate`와 `RequestPermission` 두 개입니다. 에이전트가 권한을
묻지 않는다면 `acp1.UnimplementedClient`를 임베드해 둘 다 채울 수 있습니다.

`SessionUpdate`는 `Turn` 밖의 업데이트까지 모두 받습니다. 업데이트는 한 번에 하나씩 순서대로 처리되며,
프롬프트 응답과 그 뒤에 온 에이전트 요청보다 먼저 처리됩니다. 핸들러가 느려도 연결은 계속 메시지를 읽습니다.
핸들러 안에서 에이전트를 호출하고 응답을 기다려도 되지만, 에이전트가 그 응답을 만들려고 클라이언트를 다시
호출해야 한다면 교착 상태가 됩니다.

파일 시스템·터미널·elicitation 지원은 `acp1.FileReader`, `acp1.FileWriter`,
`acp1.TerminalHandler`, `acp1.ElicitationHandler`로 추가하며, `acp1.ClientCapabilitiesOf(client)`가
대응하는 플래그를 유도합니다.

## 세션과 업데이트

### 세션 관리

```go
manager := acp1.NewSessionManager(
    acp1.NewMemoryStore[*MySession](),
    func(ctx context.Context, params *acp1.NewSessionRequest) (acp1.SessionID, *MySession, error) {
        return acp1.GenerateSessionID(), &MySession{cwd: params.Cwd}, nil
    },
)

type MyAgent struct {
    *acp1.SessionManager[*MySession] // NewSession, CancelSession, DeleteSession, ResumeSession, CloseSession 제공
}

// 선택: session/new와 session/resume 응답에 모드를 싣고, List가 세션을 설명합니다.
func (s *MySession) SessionModes() *acp1.SessionModeState {
    return &acp1.SessionModeState{CurrentModeID: s.mode, AvailableModes: myModes}
}
func (s *MySession) SessionInfo() acp1.SessionInfo { return acp1.SessionInfo{Cwd: s.cwd} }

func (a *MyAgent) ListSessions(ctx context.Context, params *acp1.ListSessionsRequest) (*acp1.ListSessionsResponse, error) {
    return a.List(ctx, params) // cwd 필터, 최근에 갱신된 순
}

// RunTurn은 세션을 찾아 턴 하나를 실행합니다. 매니저의 CancelSession이 ctx를 취소하면 턴은 cancelled로
// 응답되고, 그 사이 들어온 두 번째 프롬프트는 acp.ErrTurnInProgress를 받습니다(v1 세션은 한 번에 한 턴).
func (a *MyAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
    return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, s *MySession) (acp1.StopReason, error) {
        return acp1.StopReasonEndTurn, a.work(ctx, s)
    })
}

// RunTurnResponse는 usage처럼 stop reason 외의 필드가 있는 응답용입니다.
// 취소된 턴도 응답의 나머지 필드를 유지합니다.
return a.RunTurnResponse(ctx, params.SessionID, func(ctx context.Context, s *MySession) (*acp1.PromptResponse, error) {
    reason, err := a.work(ctx, s)
    return &acp1.PromptResponse{StopReason: reason, Usage: s.usage()}, err
})
```

에이전트에 같은 이름의 메서드를 직접 선언하면 그 메서드가 우선합니다. 세션 상태가 `SessionModesReporter`나
`SessionConfigOptionsReporter`를 구현하면 session/new와 session/resume 응답에 모드와 설정 옵션이 실립니다.
`SessionCommandsReporter`를 구현하면 그 응답 직후 슬래시 명령이 `available_commands_update`로 전송되므로,
업데이트가 가리키는 세션보다 먼저 클라이언트에 도착하는 일이 없습니다. `acp.TurnCancelled(ctx)`는
`session/cancel`로 취소된 턴을 다른 취소와 구분합니다.

매니저는 에이전트만 아는 대화를 재생해야 하는 `session/load`와, v1에서는 선택 사항인 `session/list`를 제공하지
않습니다. 목록을 지원하는 에이전트는 `ListSessions`를 `List`로 넘기고, `List`는 세션 상태의
`SessionInfoReporter`로 각 세션을 설명합니다. `List`는 페이지네이션합니다. 페이지당 최대 100개를
돌려주고 남은 세션이 있으면 다음 커서를 함께 반환하며, 페이지 크기는 `acp1.WithSessionListPageSize`로
지정합니다(0이면 한 페이지에 모두 반환).

기본 동작은 페이지마다 저장된 모든 세션을 읽고 설명하는 것입니다. 데이터베이스 기반 저장소는
`acp1.SessionInfoLister`(façade의 `SessionInfo`에 대한 `acp.SessionInfoLister`)를 구현해 한 번의 쿼리로
페이지를 응답할 수 있습니다. `ListSessionInfo`는 `acp.SessionListQuery`(cwd 필터, 시작 위치, 페이지
크기보다 하나 큰 limit)를 받아, 조건에 맞는 세션을 `acp.SessionListPosition.Compare` 순서로 반환합니다.
`updatedAt`이 최신인 세션이 먼저 오고(문자열로 비교), 시각이 없는 세션은 마지막, 같으면 세션 id
오름차순이며, 각 세션의 `SessionID`를 채워야 합니다.

| 메서드 | 동작 |
| --- | --- |
| `Lookup(ctx, id)` | 세션 상태 또는 호출자에게 반환할 오류를 돌려줌 |
| `CloseSession` | 진행 중인 턴을 취소하고, 다시 열 수 있도록 세션 유지 |
| `DeleteSession` | 세션 삭제. 없는 세션을 삭제해도 성공 |

`acp2.SessionManager`는 목록을 포함한 v2 세션 기본 메서드 전체를
같은 방식으로 제공합니다. 요청의 컨텍스트를 받고 실패할 수 있는 `acp.SessionStore[ID, T]`, `acp.MemoryStore`,
`acp.TurnTracker`는 버전과 무관한 구성 요소입니다.

`acp.FileStore`(`acp1.NewFileStore[T](dir)`)는 재시작 후에도 세션을 유지합니다. `Get`과 `List`는
`MemoryStore`처럼 메모리에서 응답하므로 상태를 제자리에서 바꿀 수 있고, `Set`할 때마다 세션을
`dir` 안의 개별 JSON 파일에 기록합니다. 매니저는 세션을 만들 때 `Set`하고, `acp1.WithAutoSave(onError)`를
주면 턴이 끝날 때마다(취소되어도) 저장합니다. `session/set_mode`처럼 턴 밖에서 바꾼 상태는
ctx의 취소와 무관하게 저장하는 `manager.Save(ctx, id, session)`으로 저장합니다. 세션은 unexported 필드를
건너뛰는 `encoding/json/v2`로 인코딩되므로, unexported 필드가 있는 세션 상태는
`MarshalJSON`/`UnmarshalJSON`을 구현합니다. 한 디렉터리는 한 번에 한 프로세스만 사용할 수 있습니다.

### SessionStream

```go
stream := acp1.NewSessionStream(client, sessionID)

stream.SendText(ctx, "안녕하세요!")
stream.StartToolCall(ctx, toolID, "파일 읽기", acp1.ToolKindRead)
stream.CompleteToolCall(ctx, toolID, acp1.WithToolContent(acp1.ToolText(contents)))
stream.ProposeToolCall(ctx, runID, "테스트 실행", acp1.ToolKindExecute) // pending: 권한 대기 중
stream.CompleteToolCall(ctx, editID, acp1.WithToolContent(acp1.ToolDiff(path, &oldText, newText)))
stream.CompleteToolCall(ctx, runID, acp1.WithToolContent(acp1.ToolTerminal(terminal.ID))) // conn.NewTerminal로 만든 터미널
stream.Send(ctx, acp1.SessionUpdateSessionInfoUpdate{Title: new("리팩터링")}) // 헬퍼가 없는 variant용
stream.WithMeta(meta).SendText(ctx, "…")                                    // 모든 알림에 _meta 첨부
```

흔한 텍스트 콘텐츠는 `acp1.TextBlock`, `acp1.TextOf`, `acp1.Texts`(프롬프트의 텍스트 블록 iterator),
`acp1.JoinTexts`(그 텍스트를 이어 붙인 문자열),
`acp1.ToolText`로, 그 밖의 도구 출력은 `acp1.ToolDiff`와 `acp1.ToolTerminal`로 다룹니다.

`stream.RunTerminal(ctx, toolID, request, timeout)`은 클라이언트 터미널에서 명령을 처음부터 끝까지
실행합니다. tool call에 터미널을 표시하고, 종료를 기다리고, 타임아웃이나 ctx 취소 시 명령을 종료하며,
터미널을 해제하기 전에 출력과 종료 상태를 돌려줍니다.

설정 옵션에는 `acp1.SelectOptions(choices...)`가 `NewSessionConfigSelectOptions`의 오류 반환 없이 select
옵션의 선택지를 만들고, `acp1.ConfigChangeOf(params)`는 `session/set_config_option` 요청을 variant와
무관하게 하나의 `ConfigChange`로 읽습니다.

새 세션에 대한 업데이트처럼 응답 뒤에 나가야 하는 알림은 핸들러에서 `acp.AfterReply(ctx, fn)`으로
등록합니다. `fn`은 응답이 큐에 들어간 뒤 실행되고, 연결은 메시지를 순서대로 씁니다.

tool call id는 세션
안에서 유일해야 하며, `acp1.GenerateToolCallID`와 `acp1.GenerateMessageID`가 `GenerateSessionID`처럼 시간순으로 정렬되는 고유 id(접두사와 UUIDv7)를
만듭니다. v2 `SessionStream`은
메시지마다 id를 받고, 명시적인 턴 상태를 위한 `Running`, `RequiresAction`, `Idle`을 제공합니다.

### 취소

나가는 호출의 컨텍스트를 취소하면 해당 요청 ID로 `$/cancel_request`를 보냅니다.
받는 쪽에서는 해당 핸들러의 컨텍스트가 취소되고, 핸들러가 먼저 응답하지 않으면
`-32800 Request cancelled`가 전달됩니다. 프롬프트 턴 전체를 취소하는
`session/cancel`과는 별개입니다([세션 관리](#세션-관리) 참고).

## 연결과 버전

### 전송 계층

stdio는 `acp.NewStdioTransport`, 원격 연결은 `acphttp.NewClientTransport` 또는
`acphttp.DialWebSocket`을 사용합니다. 실행 방법은 [HTTP 예제](../examples/README.ko.md)를 참고하세요.

`acphttp.Server`는 일반 `http.Handler`라서 인증은 앞에 두는 미들웨어로 처리하고, `serve`가 받는 context는
연결을 연 요청의 값을 유지하므로 미들웨어가 넣은 사용자 정보 등이 에이전트까지 전달됩니다. `WithErrorHandler`는
`serve`가 반환한 에러를 받고, `Server.Shutdown(ctx)`은 새 연결을 거절한 뒤 열린 연결이 끝나기를 기다렸다가
닫습니다. 클라이언트에서 거절된 요청은 HTTP 상태를 담은 `*acphttp.StatusError`로 실패하므로, 자격 증명이
만료된 401 등을 구분할 수 있습니다.

#### 재연결

재연결은 새 연결을 만드는 과정입니다: 같은 헤더와 `acphttp.WithCookieJar(jar)`로 다시 연결해 로드 밸런서의
affinity 쿠키가 같은 백엔드로 보내게 하고, `Initialize` 뒤 에이전트가 `loadSession`을 지원하면 저장해 둔
세션 id로 `LoadSession`합니다. 끊겨 있던 동안의 메시지는 재전송되지 않습니다(프로토콜 v2의 몫).
`http-client` 예제의 `-reconnect`가 이 흐름을 보여 줍니다.

#### 일시적인 연결 끊김

짧은 끊김은 전송 계층에서 처리합니다. HTTP 클라이언트는 끊긴 이벤트 스트림을 간격을 늘려 가며 다시 열고,
서버가 연결이 없다고 답하면 멈춥니다. 서버는 스트림을 새 `GET`에 넘기고, 쓰기에 실패한 메시지는 다시
보냅니다. 서버는 스트림이 5분 동안 열려 있지 않은 연결을 끝내며(`acphttp.WithIdleTimeout`), WebSocket은 양쪽이
15초마다 ping을 보내 상대가 응답하지 않으면 닫습니다(서버는 `acphttp.WithWebSocketPing`, 클라이언트는
`acphttp.WithPingInterval`).

### v1과 v2 동시 지원

```go
r := router.New().
    WithV1(func(c *acp1.AgentSideConnection) acp1.Agent { return &v1Agent{client: c} }).
    WithV2(func(c *acp2.AgentSideConnection) acp2.Agent { return &v2Agent{client: c} })
err := r.Serve(ctx, acp.NewStdioTransport(os.Stdin, os.Stdout))
```

라우터는 첫 메시지(`initialize`여야 함)를 읽어 요청 버전 이하 중 가장 높은 설정 버전을 고르고,
initialize 파라미터만 그 버전 모양으로 고칩니다(v1 전용 에이전트에 v2 요청이 오면 `info` → `clientInfo`,
`fs`/`terminal` 없음으로 다운그레이드). 이후 메시지는 그대로 전달합니다.
옵션·transport·미들웨어는 루트 `acp` 패키지에 있어 한 값으로 양쪽 파사드를 설정합니다.

두 버전을 모두 지원하는 클라이언트는 `router.NewClient`를 씁니다. 에이전트를 띄워 v2로 initialize하고,
에이전트가 `protocolVersion` 1로 응답하거나 v2 요청을 거절하면 v1으로 다시 띄웁니다. 그래서 각 버전은
자기 모양의 initialize 요청을 보냅니다:

```go
agent, err := router.NewClient().
    WithV1(newV1Client, &acp1.InitializeRequest{ClientCapabilities: v1Caps}).
    WithV2(newV2Client, &acp2.InitializeRequest{Info: info}).
    Spawn(ctx, func() *exec.Cmd { return exec.Command("my-agent") })
if err != nil {
    log.Fatal(err)
}
defer agent.Close() // Close, Wait, Done, 확장 호출은 버전과 상관없이 동작
if agent.V2 != nil {
    // agent.V2, agent.V2Init
} else {
    // agent.V1, agent.V1Init
}
```

원격 에이전트에는 `Connect`에 dial 함수를 넘깁니다. 시도할 때마다 한 번씩 호출됩니다:

```go
agent, err := router.NewClient().WithV1(…).WithV2(…).
    Connect(ctx, func(ctx context.Context) (acp.Transport, error) {
        return acphttp.NewClientTransport("https://host/acp"), nil // 또는 acphttp.DialWebSocket
    })
```

### 연결 옵션

```go
acp1.NewAgentSideConnection(newAgent, acp.NewStdioTransport(os.Stdin, os.Stdout),
    acp.WithWriteQueueSize(500),               // 쓰기 큐 크기
    acp.WithRequestTimeout(30*time.Second),    // 나가는 호출 기본 타임아웃
    acp.WithShutdownTimeout(10*time.Second),   // 셧다운 대기 한도
    acp.WithErrorHandler(func(err error) {}),  // 치명적이지 않은 에러 콜백
)
```

## 에러와 미들웨어

### 에러

핸들러에서 `acp.Err…` 생성자를 반환하면 상대에게 갈 코드를 고를 수 있고, 그 외 에러는 `-32603`이 됩니다.
상대가 보낸 에러는 `*acp.RequestError`로 돌아옵니다:

```go
if acp.IsCode(err, acp.ErrorCodeAuthRequired) {
    // 인증 후 재시도
}
```

### 미들웨어

```go
conn := acp1.NewAgentSideConnection(newAgent, acp.NewStdioTransport(os.Stdin, os.Stdout),
    acp.WithMiddleware(
        acp.LoggingMiddleware(slog.Default()), // 메서드, 소요 시간, 오류를 slog로 기록
        acp.TimeoutMiddleware(30*time.Second),
    ),
)
```

`LoggingMiddleware`는 요청을 Info, 자주 오는 session update 같은 알림을 Debug 레벨로 `method`,
`duration` 속성과 함께 기록하고, 실패하면 Warn 레벨로 `error`와 JSON-RPC `code`를 덧붙입니다.

`MetricsMiddleware`는 처리한 모든 메시지를 `acp.Metrics` 구현으로 넘겨 메서드, 종류
(`acp.RPCRequest` 또는 `acp.RPCNotification`), 소요 시간, 오류를 전달하므로 카운터·히스토그램·트레이싱을 훅 하나로 처리합니다:

```go
acp.MetricsMiddleware(acp.MetricsFunc(func(ctx context.Context, method string, kind acp.RPCKind, d time.Duration, err error) {
    rpcCalls.WithLabelValues(method, string(kind)).Inc()
    rpcDuration.WithLabelValues(method).Observe(d.Seconds())
}))
```

핸들러의 패닉은 연결이 직접 복구해 `-32603`으로 응답하므로 별도의 recovery 미들웨어가 필요하지 않습니다.

## 데이터와 확장

### Union 처리

생성된 union은 sealed variant 인터페이스를 감싸며, 타입 스위치로 읽습니다:

```go
switch update := notification.Update.Variant().(type) {
case acp1.SessionUpdateAgentMessageChunk:
    if text, ok := acp1.TextOf(update.Content); ok {
        fmt.Print(text)
    }
case acp1.SessionUpdateToolCall:
    fmt.Println(update.Title)
}

update := acp1.NewSessionUpdate(acp1.SessionUpdatePlan{Entries: entries})
```

이 SDK가 모르는 태그가 와도 메시지가 실패하지 않습니다. 스키마가 정의한 `Custom` variant가 있으면 그쪽으로,
없으면 생성된 `…Unknown` variant(예: `acp1.SessionUpdateUnknown`)로 디코드되며, `Raw` 필드에 받은 객체가
그대로 담겨 다시 인코딩할 때도 바뀌지 않습니다. `default` 케이스에서 처리하거나, 프로토콜 권고대로 무시하면 됩니다.

### 선택 필드

선택 필드는 포인터(`omitzero`)라서 명시적인 `false`나 `""`도 인코딩에서 살아남습니다. 모든 포인터 필드에는
protobuf처럼 nil-safe getter도 생성됩니다. 구조체 포인터는 그대로 돌려주므로 없는 객체를 거쳐도 호출을 이어 갈
수 있고, 그 밖의 포인터는 역참조해 필드나 receiver가 nil이면 zero value를 돌려줍니다:

```go
if init.GetAgentCapabilities().GetMCPCapabilities().GetACP() { ... }
title := params.ToolCall.GetTitle() // 없으면 ""
```

값이 없다는 사실이 zero value와 다른 의미일 때는 필드를 직접 읽으세요. 예를 들어 터미널의 `ExitCode`가 nil이면
종료 코드 0이 아니라 시그널로 끝났다는 뜻입니다.

### 확장 메서드와 `_meta`

```go
type MyAgent struct {
    acp.ExtRouter // acp.ExtMethodHandler와 acp.ExtNotificationHandler를 구현
    // ...
}

a.HandleExt("_example.com/index", func(ctx context.Context, p *IndexParams) (*IndexResult, error) {
    return &IndexResult{Files: 42}, nil
})

result, err := acp.CallExt[IndexResult](ctx, conn, "_example.com/index", IndexParams{Path: "."})
```

파라미터 디코드에 실패하면 `-32602`, 등록되지 않은 메서드는 `-32601`로 응답하고, 등록되지 않은 알림은
무시합니다. 모든 `_meta` 필드는 값을 원본 JSON 그대로 보관하는 `acp1.Meta` 타입입니다:

```go
var meta acp1.Meta
meta.Set("trace", Trace{ID: "abc"})
trace, ok, err := params.Meta.Get[Trace]("trace")
```
