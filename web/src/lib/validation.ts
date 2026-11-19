import { z } from "zod";

// The same rules as the API (internal/api/httpapi/auth_handlers.go). The API
// checks again; these exist so people see problems before submitting.

export const emailSchema = z
  .string()
  .trim()
  .min(1, "Enter your email address.")
  .max(254, "That email address is too long.")
  .regex(/^[^\s<>,;"@]+@[^\s<>,;"@]+\.[^\s<>,;"@]+$/, "Enter a valid email address.");

export const passwordSchema = z
  .string()
  .min(10, "Use at least 10 characters.")
  .max(128, "Use at most 128 characters.");

export const signupSchema = z.object({ email: emailSchema, password: passwordSchema });

export const loginSchema = z.object({
  email: z.string().trim().min(1, "Enter your email address."),
  password: z.string().min(1, "Enter your password."),
});

export const forgotPasswordSchema = z.object({ email: emailSchema });

export const resetPasswordSchema = z.object({ password: passwordSchema });

// The same rules as POST /api/vms (internal/api/httpapi/vm_handlers.go).
export const vmNameSchema = z
  .string()
  .trim()
  .min(1, "Give your PC a name.")
  .max(40, "Use at most 40 characters.");

export const linuxUsernameSchema = z
  .string()
  .regex(
    /^[a-z][a-z0-9_-]{2,30}$/,
    "Use 3 to 31 lower-case letters, digits, dashes or underscores, starting with a letter.",
  )
  .refine(
    (name) => !["root", "admin", "ubuntu", "debian", "user", "zynthera"].includes(name),
    "This username is reserved by the system. Please choose another.",
  );

export const linuxPasswordSchema = z
  .string()
  .min(8, "Use at least 8 characters.")
  .max(128, "Use at most 128 characters.");

export const vmIdentitySchema = z.object({
  name: vmNameSchema,
  linux_username: linuxUsernameSchema,
  linux_password: linuxPasswordSchema,
});

/** Flattens a zod error into one message per field. */
export function fieldErrors(error: z.ZodError): Record<string, string> {
  const out: Record<string, string> = {};
  for (const issue of error.issues) {
    const key = String(issue.path[0] ?? "form");
    if (!(key in out)) out[key] = issue.message;
  }
  return out;
}
