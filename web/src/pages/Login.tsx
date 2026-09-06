import { zodResolver } from "@hookform/resolvers/zod";
import { FC, useEffect, useState } from "react";
import { FieldValues, useForm } from "react-hook-form";
import { useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { z } from "zod";
import { fetch } from "service/http";
import { errorText } from "service/errors";
import { removeAuthToken, setAuthToken } from "utils/authStorage";
import { LoginResponse } from "types/Login";
import { ReactComponent as Logo } from "assets/logo.svg";
import { LanguageSwitcher } from "rapido-ui/LanguageSwitcher";
import { RapidoFooter } from "rapido-ui/Footer";
import { Input } from "rapido-ui/Input";
import { Button } from "rapido-ui/Button";
import { dirOf } from "utils/language";
import "rapido-ui/tailwind.css";

const schema = z.object({
  username: z.string().min(1, "login.fieldRequired"),
  password: z.string().min(1, "login.fieldRequired"),
});

export const Login: FC = () => {
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();
  const { t, i18n } = useTranslation();
  let location = useLocation();
  const {
    register,
    formState: { errors },
    handleSubmit,
  } = useForm({
    resolver: zodResolver(schema),
  });
  useEffect(() => {
    removeAuthToken();
    if (location.pathname !== "/login") {
      navigate("/login", { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const login = (values: FieldValues) => {
    setError("");
    const formData = new FormData();
    formData.append("username", values.username);
    formData.append("password", values.password);
    formData.append("grant_type", "password");
    setLoading(true);
    fetch<LoginResponse>("/admin/token", { method: "post", body: formData })
      .then(({ access_token: token }) => {
        setAuthToken(token);
        navigate("/");
      })
      .catch((err) => {
        setError(errorText(err, t("rapido.loginFailed")));
      })
      .finally(() => setLoading(false));
  };

  const usernameError = errors?.username?.message
    ? t(errors.username.message as string)
    : "";
  const passwordError = errors?.password?.message
    ? t(errors.password.message as string)
    : "";

  return (
    <div
      dir={dirOf(i18n.language)}
      lang={i18n.language}
      className="flex min-h-screen w-full flex-col justify-between bg-rapido-bg p-4 font-sans text-rapido-text sm:p-6"
    >
      {/* align="end": the trigger is pinned to the inline end of the viewport,
          so a menu hanging off its start edge sticks out past the page. */}
      <div className="flex w-full justify-end">
        <LanguageSwitcher openDirection="down" align="end" />
      </div>

      <div className="flex w-full flex-1 items-center justify-center">
        <div className="w-full max-w-[340px]">
          <div className="flex flex-col items-center gap-2">
            <Logo className="h-12 w-12 text-rapido-accent" />
            <h1 className="text-2xl font-semibold">{t("login.loginYourAccount")}</h1>
            <p className="text-rapido-muted">{t("login.welcomeBack")}</p>
          </div>

          <form onSubmit={handleSubmit(login)} className="mx-auto mt-6 max-w-[300px]">
            <div className="flex flex-col gap-3">
              <div>
                <Input
                  hasError={!!usernameError}
                  placeholder={t("username") as string}
                  {...register("username")}
                />
                {usernameError && (
                  <p className="mt-1 text-xs text-red-400">{usernameError}</p>
                )}
              </div>
              <div>
                <Input
                  type="password"
                  hasError={!!passwordError}
                  placeholder={t("password") as string}
                  {...register("password")}
                />
                {passwordError && (
                  <p className="mt-1 text-xs text-red-400">{passwordError}</p>
                )}
              </div>

              {error && (
                <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
                  {error}
                </div>
              )}

              <Button
                variant="primary"
                type="submit"
                disabled={loading}
                className="mt-1 flex w-full items-center justify-center gap-2"
              >
                {loading && (
                  <span
                    aria-hidden="true"
                    className="h-4 w-4 animate-spin rounded-full border-2 border-white/30 border-t-white"
                  />
                )}
                {loading ? t("rapido.pleaseWait") : t("login")}
              </Button>
            </div>
          </form>
        </div>
      </div>

      <RapidoFooter />
    </div>
  );
};

export default Login;
