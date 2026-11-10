import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { harness } from "./workspace-fixtures";

const events = Array.from({ length: 50 }, (_, index) => ({ id: `audit-${index}`, action: "file.upload", actorId: "admin", targetId: "file", createdAt: "2026-11-09T01:00:00Z" }));
async function openAudit(h: ReturnType<typeof harness>) {
  await h.login();
  fireEvent.click(screen.getByRole("button", { name: "管理" }));
  fireEvent.click(screen.getByRole("button", { name: "审计记录" }));
  await screen.findByText("共 123 条记录");
}
function auditHarness(command: (name: string, args?: any) => Promise<any>, data = events) {
  return harness({ admin: true, command: async (name, args) => {
    const result = await command(name, args);
    return result === undefined && name === "reserve_audit_export" ? "export-reservation" : result;
  }, request: async (_, path) => path.startsWith("/api/v1/admin/audit?") ? { events: data, total: 123 } : undefined });
}

describe("complete audit export", () => {
  it.each(["CSV", "JSON"])("exports all matching records as %s using the applied filter without renderer destinations", async format => {
    const h = auditHarness(async name => name === "export_audit" ? { status: "saved", path: `/chosen/audit.${format.toLowerCase()}`, bytes: 42000 } : undefined);
    await openAudit(h);
    fireEvent.change(screen.getByLabelText("按操作筛选"), { target: { value: "  file.upload&actorId=other  " } });
    fireEvent.click(screen.getByRole("button", { name: "查询" }));
    await waitFor(() => expect(h.request).toHaveBeenCalledWith("GET", "/api/v1/admin/audit?action=file.upload%26actorId%3Dother&limit=50&offset=0", undefined));
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(h.request.mock.calls.some(([, path]) => path.endsWith("offset=50"))).toBe(true));
    fireEvent.change(screen.getByLabelText("按操作筛选"), { target: { value: "unsaved.filter" } });
    fireEvent.click(screen.getByRole("button", { name: `导出全部 ${format}` }));
    await screen.findByText(`已保存：/chosen/audit.${format.toLowerCase()}`);
    expect(h.invoke).toHaveBeenCalledWith("export_audit", { exportId: "export-reservation", format: format.toLowerCase(), action: "file.upload&actorId=other" });
    expect(h.request.mock.calls.some(([, path]) => path.includes("/audit/export"))).toBe(false);
  });

  it("shows busy state, prevents duplicate exports and waits for cancellation cleanup", async () => {
    let finish!: (result: unknown) => void;
    const h = auditHarness(async name => {
      if (name === "export_audit") return new Promise(resolve => { finish = resolve; });
      if (name === "cancel_audit_export") return null;
    });
    await openAudit(h);
    fireEvent.click(screen.getByRole("button", { name: "导出全部 CSV" }));
    await waitFor(() => expect(h.invoke.mock.calls.some(([name]) => name === "export_audit")).toBe(true));
    expect(screen.getByRole("button", { name: "导出全部 CSV" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "导出全部 JSON" })).toBeDisabled();
    expect(screen.getByText("正在选择位置或导出全部记录…")).toHaveAttribute("role", "status");
    fireEvent.click(screen.getByRole("button", { name: "取消导出" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("cancel_audit_export", { exportId: "export-reservation" }));
    expect(screen.queryByText("已取消导出。")).not.toBeInTheDocument();
    await act(async () => finish({ status: "cancelled" }));
    expect(screen.getByText("已取消导出。")).toHaveAttribute("role", "status");
    expect(screen.getByRole("button", { name: "导出全部 CSV" })).toBeEnabled();
    expect(screen.queryByText(/已保存/)).not.toBeInTheDocument();
  });

  it("handles picker cancellation without claiming a save", async () => {
    const h = auditHarness(async name => name === "export_audit" ? { status: "cancelled" } : undefined);
    await openAudit(h);
    fireEvent.click(screen.getByRole("button", { name: "导出全部 JSON" }));
    await screen.findByText("已取消导出。");
    expect(screen.queryByText(/已保存/)).not.toBeInTheDocument();
  });

  it("shows native errors and allows a fresh attempt", async () => {
    let fail = true;
    const h = auditHarness(async name => {
      if (name === "export_audit") {
        if (fail) throw { error: "目标文件已存在，请选择其他名称" };
        return { status: "saved", path: "/chosen/new.csv", bytes: 9000 };
      }
    });
    await openAudit(h);
    fireEvent.click(screen.getByRole("button", { name: "导出全部 CSV" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("目标文件已存在");
    expect(screen.queryByText(/已保存/)).not.toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "导出全部 CSV" }));
    await screen.findByText("已保存：/chosen/new.csv");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("cancels an active export when leaving the audit view", async () => {
    let finish!: (result: unknown) => void;
    const h = auditHarness(async name => {
      if (name === "export_audit") return new Promise(resolve => { finish = resolve; });
      if (name === "cancel_audit_export") return null;
    });
    await openAudit(h);
    fireEvent.click(screen.getByRole("button", { name: "导出全部 CSV" }));
    await waitFor(() => expect(h.invoke.mock.calls.some(([name]) => name === "export_audit")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "消息" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("cancel_audit_export", { exportId: "export-reservation" }));
    await act(async () => finish({ status: "cancelled" }));
    expect(screen.queryByText("已取消导出。")).not.toBeInTheDocument();
  });

  it.each(["cancel", "navigate"])("remembers %s while the native reservation reply is pending and never starts export", async intent => {
    let reserve!: (value: string) => void;
    const h = auditHarness(async name => {
      if (name === "reserve_audit_export") return new Promise<string>(resolve => { reserve = resolve; });
      if (name === "cancel_audit_export") return null;
      if (name === "export_audit") return { status: "saved", path: "/must-not-exist.csv", bytes: 42 };
    });
    await openAudit(h);
    fireEvent.click(screen.getByRole("button", { name: "导出全部 CSV" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("reserve_audit_export"));
    fireEvent.click(screen.getByRole("button", { name: intent === "cancel" ? "取消导出" : "消息" }));
    await act(async () => reserve("late-reservation"));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("cancel_audit_export", { exportId: "late-reservation" }));
    expect(h.invoke.mock.calls.some(([name]) => name === "export_audit")).toBe(false);
    if (intent === "cancel") await screen.findByText("已取消导出。");
    expect(screen.queryByText(/已保存/)).not.toBeInTheDocument();
  });

  it("escapes formula-like cells in retained current-page CSV previews", async () => {
    const h = auditHarness(async () => undefined, [{ ...events[0], actorId: '  =HYPERLINK("https://example.com")' }]);
    await openAudit(h);
    fireEvent.click(screen.getByRole("button", { name: "查看本页 CSV" }));
    expect((screen.getByLabelText("当前页审计数据（CSV）") as HTMLTextAreaElement).value).toContain("\"'  =HYPERLINK(\"\"https://example.com\"\")\"");
  });
});
