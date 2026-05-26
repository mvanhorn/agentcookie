// Homepage footer - server component.
//
// One row of repo + docs links. The threat-model link is the
// signal that this is a serious-security project, not a casual
// cookie syncer.

const GITHUB = "https://github.com/mvanhorn/agentcookie";

export function Footer() {
  return (
    <footer className="flex flex-col gap-3 border-t border-border-0 py-6 pb-12">
      <div className="flex flex-wrap items-center gap-3 text-[13px] text-text-1">
        <a href={GITHUB} className="hover:text-text-0">
          github.com/mvanhorn/agentcookie
        </a>
        <span className="text-text-2">·</span>
        <a
          href={`${GITHUB}/blob/main/docs/quickstart.md`}
          className="hover:text-text-0"
        >
          quickstart
        </a>
        <span className="text-text-2">·</span>
        <a
          href={`${GITHUB}/blob/main/docs/spec-agentcookie-secrets-bus-v1.md`}
          className="hover:text-text-0"
        >
          secrets bus v1 spec
        </a>
        <span className="text-text-2">·</span>
        <a
          href={`${GITHUB}/blob/main/docs/spec-agentcookie-secrets-bus-v2-adoption.md`}
          className="hover:text-text-0"
        >
          v2 adoption spec
        </a>
        <span className="text-text-2">·</span>
        <a
          href={`${GITHUB}/blob/main/docs/threat-model.md`}
          className="hover:text-text-0"
        >
          threat model
        </a>
      </div>
      <div className="text-[13px] text-text-2">
        MIT licensed. macOS only.
      </div>
    </footer>
  );
}

export default Footer;
