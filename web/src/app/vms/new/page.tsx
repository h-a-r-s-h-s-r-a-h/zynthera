"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { RequireAuth } from "@/components/auth-guard";
import { Button, ButtonLink, Card, Choice, Field, Notice, PageLoading } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import type { Options, OSImage } from "@/lib/api/types";
import { useCreateVM, useImages, useOptions } from "@/lib/api/vms";
import { fieldErrors, vmIdentitySchema } from "@/lib/validation";
import { formatRAM } from "@/lib/vm-state";

const steps = ["Operating system", "Size", "Name and login", "Review"] as const;

function StepHeader({ step }: { step: number }) {
  return (
    <ol className="flex flex-wrap gap-x-4 gap-y-1 text-sm" aria-label="Steps">
      {steps.map((label, i) => (
        <li
          key={label}
          aria-current={i === step ? "step" : undefined}
          className={i === step ? "font-semibold text-text" : "text-text-muted"}
        >
          {i + 1}. {label}
        </li>
      ))}
    </ol>
  );
}

/** How much of a limit is left once the user's other PCs are counted. */
function remaining(options: Options) {
  return {
    ram: options.limits.max_ram_mb - options.usage.ram_mb,
    vcpus: options.limits.max_vcpus - options.usage.vcpus,
    disk: options.limits.max_disk_gb - options.usage.disk_gb,
    vms: options.limits.max_vms - options.usage.vms,
  };
}

function Wizard({ images, options }: { images: OSImage[]; options: Options }) {
  const router = useRouter();
  const create = useCreateVM();
  const left = remaining(options);

  const [step, setStep] = useState(0);
  const [image, setImage] = useState(images[0]?.slug ?? "");
  const [ram, setRam] = useState(() => {
    const fits = options.allowed_ram_mb.filter((v) => v <= left.ram);
    return fits.includes(4096) ? 4096 : (fits[0] ?? options.allowed_ram_mb[0]);
  });
  const [vcpus, setVcpus] = useState(() => {
    const fits = options.allowed_vcpus.filter((v) => v <= left.vcpus);
    return fits.includes(2) ? 2 : (fits[0] ?? options.allowed_vcpus[0]);
  });
  const [disk, setDisk] = useState(() => {
    const fits = options.allowed_disk_gb.filter((v) => v <= left.disk);
    return fits.includes(80) ? 80 : (fits[0] ?? options.allowed_disk_gb[0]);
  });
  const [name, setName] = useState("My PC");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");

  const chosenImage = images.find((i) => i.slug === image);

  if (left.vms <= 0) {
    return (
      <Card className="flex flex-col items-start gap-3">
        <p className="text-lg font-medium">You have reached your PC limit</p>
        <p className="text-sm text-text-muted">
          Your account allows {options.limits.max_vms} {options.limits.max_vms === 1 ? "PC" : "PCs"}
          . Delete one to create another.
        </p>
        <ButtonLink href="/dashboard" variant="secondary">
          Back to your PCs
        </ButtonLink>
      </Card>
    );
  }
  if (images.length === 0) {
    return (
      <Card className="flex flex-col items-start gap-3">
        <p className="text-lg font-medium">No PCs can be created right now</p>
        <p className="text-sm text-text-muted">
          Our servers are not available at the moment. Please try again in a few minutes.
        </p>
        <ButtonLink href="/dashboard" variant="secondary">
          Back to your PCs
        </ButtonLink>
      </Card>
    );
  }

  function validateIdentity(): boolean {
    const parsed = vmIdentitySchema.safeParse({
      name,
      linux_username: username,
      linux_password: password,
    });
    setErrors(parsed.success ? {} : fieldErrors(parsed.error));
    return parsed.success;
  }

  function next(event: FormEvent) {
    event.preventDefault();
    setFormError("");
    if (step === 2 && !validateIdentity()) return;
    if (step < steps.length - 1) {
      setStep(step + 1);
      return;
    }
    create.mutate(
      {
        name: name.trim(),
        image,
        ram_mb: ram,
        vcpus,
        disk_gb: disk,
        linux_username: username,
        linux_password: password,
      },
      {
        // The viewer shows progress, then the first boot.
        onSuccess: (res) => router.push(`/vms/${res.vm.id}/desktop`),
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length > 0) {
            setErrors(err.fields);
            // Go back to the step that holds the first bad field.
            const sizeField = ["ram_mb", "vcpus", "disk_gb"].some((f) => f in err.fields);
            setStep("image" in err.fields ? 0 : sizeField ? 1 : 2);
          } else {
            // Includes "no capacity": every choice is kept, so the user can
            // simply try again.
            setFormError(err.message);
          }
        },
      },
    );
  }

  return (
    <form onSubmit={next} noValidate className="flex flex-col gap-6">
      <StepHeader step={step} />
      <Card className="flex flex-col gap-5">
        {step === 0 && (
          <div className="flex flex-col gap-3">
            <h2 className="text-lg font-semibold">Choose an operating system</h2>
            <div
              role="radiogroup"
              aria-label="Operating system"
              className="grid gap-2 sm:grid-cols-2"
            >
              {images.map((img) => (
                <Choice
                  key={img.slug}
                  selected={image === img.slug}
                  title={img.display_name}
                  hint={img.description}
                  onSelect={() => setImage(img.slug)}
                />
              ))}
            </div>
            {errors.image && <p className="text-sm text-danger">{errors.image}</p>}
          </div>
        )}

        {step === 1 && (
          <div className="flex flex-col gap-5">
            <h2 className="text-lg font-semibold">Choose a size</h2>
            <SizeGroup
              label="Memory"
              left={`${formatRAM(Math.max(left.ram, 0))} left on your account`}
              error={errors.ram_mb}
            >
              {options.allowed_ram_mb.map((v) => (
                <Choice
                  key={v}
                  selected={ram === v}
                  disabled={v > left.ram || v < (chosenImage?.min_ram_mb ?? 0)}
                  title={formatRAM(v)}
                  onSelect={() => setRam(v)}
                />
              ))}
            </SizeGroup>
            <SizeGroup
              label="CPU cores"
              left={`${Math.max(left.vcpus, 0)} left on your account`}
              error={errors.vcpus}
            >
              {options.allowed_vcpus.map((v) => (
                <Choice
                  key={v}
                  selected={vcpus === v}
                  disabled={v > left.vcpus}
                  title={String(v)}
                  onSelect={() => setVcpus(v)}
                />
              ))}
            </SizeGroup>
            <SizeGroup
              label="Disk"
              left={`${Math.max(left.disk, 0)} GB left on your account`}
              error={errors.disk_gb}
            >
              {options.allowed_disk_gb.map((v) => (
                <Choice
                  key={v}
                  selected={disk === v}
                  disabled={v > left.disk || v < (chosenImage?.min_disk_gb ?? 0)}
                  title={`${v} GB`}
                  onSelect={() => setDisk(v)}
                />
              ))}
            </SizeGroup>
            <p className="text-sm text-text-muted">
              Memory and CPU can be changed later while the PC is shut down. A disk can grow but not
              shrink.
            </p>
          </div>
        )}

        {step === 2 && (
          <div className="flex flex-col gap-4">
            <h2 className="text-lg font-semibold">Name your PC and set its login</h2>
            <Field
              id="name"
              label="PC name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              error={errors.name}
              maxLength={40}
            />
            <Field
              id="linux_username"
              label="Linux username"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              error={errors.linux_username}
              hint="The account you are logged in as inside the PC."
            />
            <Field
              id="linux_password"
              label="Linux password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              error={errors.linux_password}
              hint="You need this password for sudo, for example to install software. We cannot show it to you later."
            />
          </div>
        )}

        {step === 3 && (
          <div className="flex flex-col gap-4">
            <h2 className="text-lg font-semibold">Review</h2>
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-text-muted">Name</dt>
              <dd className="font-medium">{name.trim()}</dd>
              <dt className="text-text-muted">Operating system</dt>
              <dd className="font-medium">{chosenImage?.display_name}</dd>
              <dt className="text-text-muted">Memory</dt>
              <dd className="font-medium">{formatRAM(ram)}</dd>
              <dt className="text-text-muted">CPU cores</dt>
              <dd className="font-medium">{vcpus}</dd>
              <dt className="text-text-muted">Disk</dt>
              <dd className="font-medium">{disk} GB</dd>
              <dt className="text-text-muted">Linux username</dt>
              <dd className="font-medium">{username}</dd>
            </dl>
            {formError && <Notice tone="danger">{formError}</Notice>}
          </div>
        )}
      </Card>

      <div className="flex items-center justify-between gap-3">
        {step === 0 ? (
          <ButtonLink href="/dashboard" variant="ghost">
            Cancel
          </ButtonLink>
        ) : (
          <Button type="button" variant="secondary" onClick={() => setStep(step - 1)}>
            Back
          </Button>
        )}
        <Button type="submit" busy={create.isPending}>
          {step === steps.length - 1 ? "Create PC" : "Continue"}
        </Button>
      </div>
    </form>
  );
}

function SizeGroup({
  label,
  left,
  error,
  children,
}: {
  label: string;
  left: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-sm font-medium">{label}</span>
        <span className="text-xs text-text-muted">{left}</span>
      </div>
      <div role="radiogroup" aria-label={label} className="flex flex-wrap gap-2">
        {children}
      </div>
      {error && <p className="text-sm text-danger">{error}</p>}
    </div>
  );
}

function NewVM() {
  const images = useImages();
  const options = useOptions();
  if (images.isError || options.isError) {
    return <Notice tone="danger">{(images.error ?? options.error)?.message}</Notice>;
  }
  if (!images.data || !options.data) return <PageLoading />;
  return <Wizard images={images.data} options={options.data} />;
}

export default function NewVMPage() {
  return (
    <RequireAuth>
      <section className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-6 px-4 py-10">
        <h1 className="text-2xl font-semibold tracking-tight">Create a PC</h1>
        <NewVM />
      </section>
    </RequireAuth>
  );
}
