import { fireEvent, screen, waitFor } from "@testing-library/react";
import { harness } from "./workspace-fixtures";

const poolFile = {
  id: "pooled-file", groupId: "original-private-group", name: "公共规范.pdf", size: 100,
  createdBy: "u1", visibility: "pool", currentVersionId: "pool-v1", status: "active",
  createdAt: "2026-11-09", expiresAt: null,
};

describe("pool version upload", () => {
  it("lets an admin outside the original group queue a replacement in that group", async () => {
    const h = harness({
      admin: true,
      request: async (_, path) => path.includes("scope=pool") ? [poolFile] : undefined,
      command: async name => name === "pick_upload_files" ? [{id:"new-standard-handle",name:"new-standard.pdf",size:12}] : name === "start_attachment_upload" ? "replacement-task" : undefined,
    });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "文件" }));
    await screen.findByText(poolFile.name);

    fireEvent.click(screen.getByRole("button", { name: "上传新版本" }));

    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("start_attachment_upload", {
      groupId: "original-private-group", sourceHandle: "new-standard-handle", clientAttachmentId:expect.any(String), fileId: "pooled-file",
    }));
    expect(h.invoke).toHaveBeenCalledWith("pick_upload_files");
    expect(await screen.findByText("上传已加入传输队列，完成后将显示在文件列表中。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "上传文件" })).not.toBeInTheDocument();
  });

  it("does not queue a replacement when the admin cancels the file picker", async () => {
    const h = harness({
      admin: true,
      request: async (_, path) => path.includes("scope=pool") ? [poolFile] : undefined,
      command: async name => name === "pick_upload_files" ? [] : undefined,
    });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "文件" }));
    await screen.findByText(poolFile.name);

    fireEvent.click(screen.getByRole("button", { name: "上传新版本" }));

    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("pick_upload_files"));
    await waitFor(() => expect(screen.getByRole("button", { name: "上传新版本" })).toBeEnabled());
    expect(h.invoke.mock.calls.some(([name]) => name === "start_attachment_upload")).toBe(false);
    expect(screen.queryByText("上传已加入传输队列，完成后将显示在文件列表中。")).not.toBeInTheDocument();
  });

  it("does not expose pool mutation to a nonadmin uploader and group owner", async () => {
    const h = harness({ request: async (_, path) => {
      if (path.endsWith("/members")) return [{ userId: "u1", role: "owner", joinedAt: "2026-11-09" }];
      if (path.includes("/files?")) return [{ ...poolFile, groupId: "g1" }];
    } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "文件" }));
    await screen.findByText(poolFile.name);

    expect(screen.queryByRole("button", { name: "上传新版本" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "删除" })).not.toBeInTheDocument();
    expect(h.invoke.mock.calls.some(([name]) => name === "pick_upload_files" || name === "start_attachment_upload")).toBe(false);
  });
});
