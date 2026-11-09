import { act, render, screen } from "@testing-library/react";
import { App } from "../src/main";

describe("app shell", () => {
  beforeEach(() => {
    window.__APP_VERSION__ = "0.1.0-test";
  });

  it("renders the real Chinese login and app version", async () => {
    render(<App />);

    expect(screen.getByTestId("login-route")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "登录工作区" })).toBeInTheDocument();
    expect(screen.getByTestId("app-version")).toHaveTextContent("0.1.0-test");
    expect(screen.getByTestId("app-version").closest("footer")).toHaveClass("ln-login-footer");
    await act(async () => { await Promise.resolve(); });
  });
});
