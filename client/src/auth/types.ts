export interface User {
  id: string;
  accountNumber?: string;
  displayName?: string;
  avatarUrl?: string;
  username: string;
  email: string;
  roles: string[];
  disabled: boolean;
  mustChangePassword: boolean;
}

export interface Session {
  id: string;
  accessToken?: string;
  refreshToken?: string;
  expiresAt: string;
  csrfToken?: string;
  capabilities?: Record<string, boolean | number>;
  user: User;
  deviceId: string;
}

export interface LoginInput {
  serverUrl?: string;
  username: string;
  password: string;
  deviceId?: string;
}
