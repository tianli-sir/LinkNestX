import { render, screen } from "@testing-library/react";
import { App } from "../src/main";

describe("app shell", () => {
  beforeEach(() => {
    window.__APP_VERSION__ = "0.1.0-test";
  });

  it("renders the login route placeholder and app version", () => {
    render(<App />);

    expect(screen.getByTestId("login-route")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /login/i })).toBeInTheDocument();
    expect(screen.getByTestId("app-version")).toHaveTextContent("0.1.0-test");
  });
});
