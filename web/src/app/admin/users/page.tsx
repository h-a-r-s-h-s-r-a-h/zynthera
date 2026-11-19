"use client";

import { AdminOnly } from "@/components/admin-only";
import { useState, type FormEvent } from "react";
import { Button, Notice, PageLoading } from "@/components/ui";
import { useAdminUsers, usePatchUser, type UserPatch } from "@/lib/api/admin";
import { useMe } from "@/lib/api/auth";
import { ApiError } from "@/lib/api/client";
import type { AdminUser, UserLimitDefaults } from "@/lib/api/types";

const limitKeys = ["max_vms", "max_ram_mb", "max_vcpus", "max_disk_gb"] as const;
type LimitKey = (typeof limitKeys)[number];

const limitLabel: Record<LimitKey, string> = {
  max_vms: "PCs",
  max_ram_mb: "RAM (MB)",
  max_vcpus: "vCPUs",
  max_disk_gb: "Disk (GB)",
};

const control = "h-9 rounded-control border border-border bg-bg px-2 text-sm text-text";

function LimitsEditor({
  user,
  defaults,
  onClose,
}: {
  user: AdminUser;
  defaults: UserLimitDefaults;
  onClose: () => void;
}) {
  const patch = usePatchUser();
  // An empty box means "use the default".
  const [values, setValues] = useState<Record<LimitKey, string>>(() => ({
    max_vms: user.max_vms?.toString() ?? "",
    max_ram_mb: user.max_ram_mb?.toString() ?? "",
    max_vcpus: user.max_vcpus?.toString() ?? "",
    max_disk_gb: user.max_disk_gb?.toString() ?? "",
  }));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    const body: UserPatch = {};
    const problems: Record<string, string> = {};
    for (const key of limitKeys) {
      const raw = values[key].trim();
      if (raw === "") {
        body[key] = null;
      } else if (/^\d+$/.test(raw)) {
        body[key] = Number(raw);
      } else {
        problems[key] = "Whole number, or empty for the default.";
      }
    }
    setErrors(problems);
    setFormError("");
    if (Object.keys(problems).length > 0) return;
    patch.mutate(
      { id: user.id, patch: body },
      {
        onSuccess: onClose,
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length > 0) setErrors(err.fields);
          else setFormError(err.message);
        },
      },
    );
  }

  return (
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-3 bg-surface p-3">
      <p className="text-sm text-text-muted">
        Limits for <span className="font-medium text-text">{user.email}</span>, summed over all
        their PCs. Leave a box empty to use the default.
      </p>
      {formError && <Notice tone="danger">{formError}</Notice>}
      <div className="flex flex-wrap gap-3">
        {limitKeys.map((key) => (
          <label key={key} className="flex flex-col gap-1 text-sm">
            <span className="text-text-muted">{limitLabel[key]}</span>
            <input
              className={`${control} w-32 ${errors[key] ? "border-danger" : ""}`}
              inputMode="numeric"
              placeholder={`default ${defaults[key]}`}
              value={values[key]}
              onChange={(e) => setValues((v) => ({ ...v, [key]: e.target.value }))}
              aria-invalid={errors[key] ? true : undefined}
            />
            {errors[key] && <span className="max-w-32 text-xs text-danger">{errors[key]}</span>}
          </label>
        ))}
      </div>
      <div className="flex gap-2">
        <Button type="submit" busy={patch.isPending}>
          Save limits
        </Button>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function AdminUsersPageContent() {
  const me = useMe();
  const [email, setEmail] = useState("");
  const [emailDraft, setEmailDraft] = useState("");
  const [editing, setEditing] = useState<string | null>(null);
  const users = useAdminUsers(email);
  const patch = usePatchUser();

  const rows = users.data?.pages.flatMap((page) => page.users) ?? [];
  const defaults = users.data?.pages[0]?.defaults;

  return (
    <div className="flex flex-col gap-4">
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          setEmail(emailDraft.trim());
        }}
      >
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-text-muted">Email contains</span>
          <input
            className={control}
            value={emailDraft}
            onChange={(e) => setEmailDraft(e.target.value)}
          />
        </label>
        <Button type="submit" variant="secondary">
          Search
        </Button>
      </form>

      {patch.isError && <Notice tone="danger">{patch.error.message}</Notice>}
      {users.isError ? (
        <Notice tone="danger">{users.error.message}</Notice>
      ) : !users.data || !defaults ? (
        <PageLoading />
      ) : rows.length === 0 ? (
        <Notice>No users match.</Notice>
      ) : (
        <div className="overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[44rem] text-left text-sm">
            <thead className="border-b border-border bg-surface text-text-muted">
              <tr>
                <th className="px-3 py-2 font-medium">User</th>
                <th className="px-3 py-2 font-medium">PCs</th>
                <th className="px-3 py-2 font-medium">Limits</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rows.map((user) => (
                <UserRow
                  key={user.id}
                  user={user}
                  defaults={defaults}
                  isSelf={user.id === me.data?.id}
                  editing={editing === user.id}
                  onEdit={(open) => setEditing(open ? user.id : null)}
                  onToggleDisabled={() =>
                    patch.mutate({ id: user.id, patch: { disabled: !user.disabled } })
                  }
                  busy={patch.isPending}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {users.hasNextPage && (
        <div>
          <Button
            variant="secondary"
            busy={users.isFetchingNextPage}
            onClick={() => void users.fetchNextPage()}
          >
            Load more
          </Button>
        </div>
      )}
    </div>
  );
}

function UserRow({
  user,
  defaults,
  isSelf,
  editing,
  onEdit,
  onToggleDisabled,
  busy,
}: {
  user: AdminUser;
  defaults: UserLimitDefaults;
  isSelf: boolean;
  editing: boolean;
  onEdit: (open: boolean) => void;
  onToggleDisabled: () => void;
  busy: boolean;
}) {
  const custom = limitKeys.filter((key) => user[key] !== null);
  return (
    <>
      <tr data-testid="admin-user-row">
        <td className="max-w-64 px-3 py-2">
          <div className="truncate font-medium" title={user.email}>
            {user.email}
          </div>
          <div className="text-xs text-text-muted">
            {user.role === "admin" ? "Administrator" : "User"}
            {!user.email_verified && " · email not confirmed"}
          </div>
        </td>
        <td className="px-3 py-2 whitespace-nowrap">
          {user.vm_count} · {Math.round(user.used_ram_mb / 1024)} GB RAM · {user.used_disk_gb} GB
          disk
        </td>
        <td className="px-3 py-2">
          {custom.length === 0
            ? "Defaults"
            : custom.map((key) => `${limitLabel[key]}: ${user[key]}`).join(", ")}
        </td>
        <td className="px-3 py-2">
          {user.disabled ? (
            <span className="text-danger">Disabled</span>
          ) : (
            <span className="text-success">Active</span>
          )}
        </td>
        <td className="px-3 py-2">
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => onEdit(!editing)}>
              {editing ? "Close" : "Limits"}
            </Button>
            {!isSelf && (
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => {
                  const ok =
                    user.disabled ||
                    window.confirm(
                      `Disable ${user.email}? They are logged out and their running PCs are suspended. Nothing is deleted.`,
                    );
                  if (ok) onToggleDisabled();
                }}
              >
                {user.disabled ? "Enable" : "Disable"}
              </Button>
            )}
          </div>
        </td>
      </tr>
      {editing && (
        <tr>
          <td colSpan={5} className="p-0">
            <LimitsEditor user={user} defaults={defaults} onClose={() => onEdit(false)} />
          </td>
        </tr>
      )}
    </>
  );
}

export default function AdminUsersPage() {
  return (
    <AdminOnly>
      <AdminUsersPageContent />
    </AdminOnly>
  );
}
