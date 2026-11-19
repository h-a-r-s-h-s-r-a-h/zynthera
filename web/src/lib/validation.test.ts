import { describe, expect, it } from "vitest";
import { fieldErrors, loginSchema, signupSchema } from "./validation";

describe("signupSchema", () => {
  it("accepts a normal address and trims it", () => {
    const result = signupSchema.safeParse({
      email: "  alex@example.com ",
      password: "a-good-password",
    });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.email).toBe("alex@example.com");
  });

  it.each(["", "alex", "alex@", "alex@example", "a b@example.com", "Alex <alex@example.com>"])(
    "rejects the address %j",
    (email) => {
      expect(signupSchema.safeParse({ email, password: "a-good-password" }).success).toBe(false);
    },
  );

  it("enforces the password length limits", () => {
    expect(signupSchema.safeParse({ email: "a@example.com", password: "123456789" }).success).toBe(
      false,
    );
    expect(signupSchema.safeParse({ email: "a@example.com", password: "1234567890" }).success).toBe(
      true,
    );
    expect(
      signupSchema.safeParse({ email: "a@example.com", password: "x".repeat(129) }).success,
    ).toBe(false);
  });
});

describe("fieldErrors", () => {
  it("gives one message per field", () => {
    const result = loginSchema.safeParse({ email: "", password: "" });
    expect(result.success).toBe(false);
    if (!result.success) {
      expect(fieldErrors(result.error)).toEqual({
        email: "Enter your email address.",
        password: "Enter your password.",
      });
    }
  });
});
