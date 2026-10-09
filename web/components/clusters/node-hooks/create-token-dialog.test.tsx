// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CreateTokenDialog } from "@/components/clusters/node-hooks/create-token-dialog";

const SECRET = "cnt_super-secret-token-value";

afterEach(() => { cleanup(); });

describe("CreateTokenDialog", () => {
  it("validates the name before enabling Create", async () => {
    const user = userEvent.setup();
    render(<CreateTokenDialog onCreate={vi.fn()} onCreated={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Create token" }));
    const create = screen.getByRole("button", { name: "Create" });
    expect(create).toBeDisabled();
    await user.type(screen.getByLabelText("Token name"), "Bad Name");
    expect(create).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent(/lowercase/);
    await user.clear(screen.getByLabelText("Token name"));
    await user.type(screen.getByLabelText("Token name"), "rack-1");
    expect(create).toBeEnabled();
  });

  it("shows the token once and drops it when the dialog closes", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn().mockResolvedValue({ ok: true, created: { id: "1", name: "rack-1", token: SECRET } });
    const onCreated = vi.fn();
    render(<CreateTokenDialog onCreate={onCreate} onCreated={onCreated} />);
    await user.click(screen.getByRole("button", { name: "Create token" }));
    await user.type(screen.getByLabelText("Token name"), "rack-1");
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(onCreate).toHaveBeenCalledWith("rack-1");
    expect(onCreated).toHaveBeenCalledTimes(1);
    expect(await screen.findByDisplayValue(SECRET)).toBeInTheDocument();
    expect(screen.getByText("This token will not be shown again.")).toBeInTheDocument();
    expect(screen.getByText(/CUSTOS_TOKEN_FILE=/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);

    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => { expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument(); });
    expect(document.body.innerHTML).not.toContain(SECRET);

    // Reopening starts from an empty form: the secret is not retained.
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Token name")).toHaveValue("");
    expect(document.body.innerHTML).not.toContain(SECRET);
  });

  it("shows server errors and keeps the form usable", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn().mockResolvedValue({ ok: false, message: "name: taken" });
    render(<CreateTokenDialog onCreate={onCreate} onCreated={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Create token" }));
    await user.type(screen.getByLabelText("Token name"), "rack-1");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("name: taken")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create" })).toBeEnabled();
  });
});
