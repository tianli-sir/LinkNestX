import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { invoke } from "@tauri-apps/api/core";
import { emit } from "@tauri-apps/api/event";
import { clearMocks, mockIPC } from "@tauri-apps/api/mocks";
import { useState } from "react";
import { DesktopProvider, useDesktop, useDesktopEvent, type DesktopAdapter } from "../src/runtime/DesktopProvider";

beforeEach(() => {
  mockIPC(() => undefined, { shouldMockEvents: true });
  window.__TAURI__ = { core: { invoke } };
});

afterEach(() => {
  cleanup();
  delete window.__TAURI__;
  clearMocks();
});

describe("real desktop event adapter", () => {
  it.each([
    ["server.event", "server:event"],
    ["connection.changed", "connection:changed"],
    ["transfer.updated", "transfer:updated"],
    ["chunk.verified", "chunk:verified"],
    ["sharing.changed", "sharing:changed"],
    ["sharing.revoked", "sharing:revoked"],
    ["service.error", "service:error"],
  ])("receives %s through the native %s channel without rewriting payloads", async (logicalName, wireName) => {
    let desktop!: DesktopAdapter;
    function Consumer() {
      desktop = useDesktop().desktop;
      return null;
    }
    render(<DesktopProvider><Consumer /></DesktopProvider>);
    const received: unknown[] = [];
    const remove = await desktop.listen(logicalName, payload => received.push(payload));
    const payload = { type: "message.created", message: { body: "规范.v2.pdf" } };

    await act(async () => { await emit(wireName, payload); });

    expect(received).toEqual([payload]);
    remove();
  });

  it("updates connection and logical subscribers from native events", async () => {
    function Consumer() {
      const { connection } = useDesktop();
      const [message, setMessage] = useState("");
      const [transferState, setTransferState] = useState("");
      useDesktopEvent("server.event", payload => {
        if (payload.type === "message.created") setMessage(payload.message.body);
      });
      useDesktopEvent("transfer.updated", payload => setTransferState(payload.state));
      return <>
        <output aria-label="连接状态">{connection.status}</output>
        <output aria-label="收到的消息">{message}</output>
        <output aria-label="传输状态">{transferState}</output>
      </>;
    }
    render(<DesktopProvider><Consumer /></DesktopProvider>);

    await act(async () => { await emit("connection:changed", { status: "disconnected" }); });
    expect(screen.getByLabelText("连接状态")).toHaveTextContent("disconnected");
    await act(async () => { await emit("server:event", { type: "session.ready", localResync: true }); });
    expect(screen.getByLabelText("连接状态")).toHaveTextContent("disconnected");
    await act(async () => { await emit("server:event", { type: "session.ready" }); });
    expect(screen.getByLabelText("连接状态")).toHaveTextContent("connected");
    await act(async () => {
      await emit("server:event", { type: "message.created", message: { body: "来自桌面服务的消息" } });
      await emit("transfer:updated", { state: "completed" });
    });
    expect(screen.getByLabelText("收到的消息")).toHaveTextContent("来自桌面服务的消息");
    expect(screen.getByLabelText("传输状态")).toHaveTextContent("completed");
  });

  it("shows listener registration failures in connection state", async () => {
    mockIPC(() => { throw new Error("无法订阅桌面事件"); });
    function Consumer() {
      const { connection } = useDesktop();
      return <output>{connection.status}: {connection.label}</output>;
    }
    render(<DesktopProvider><Consumer /></DesktopProvider>);

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("error: 无法订阅桌面事件"));
  });
});

it.each(['service.error','sharing.changed','sharing.revoked','sharing.activity','peer.sent','service.status'])('provider subscribers receive %s without a direct adapter listener',async(name)=>{
 function Consumer(){const [received,setReceived]=useState('');useDesktopEvent(name,v=>setReceived(v.marker));return <output aria-label="支持的后台事件">{received}</output>}
 render(<DesktopProvider><Consumer/></DesktopProvider>);await act(async()=>{await emit(name.replaceAll('.',':'),{marker:'received'})});expect(screen.getByLabelText('支持的后台事件')).toHaveTextContent('received');
});
