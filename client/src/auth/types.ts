export interface User {
  id: string;
  username: string;
  email: string;
  roles: string[];
  disabled: boolean;
  mustChangePassword: boolean;
}

export interface Session {
  id: string;
  accessToken: string;
  refreshToken: string;
  expiresAt: string;
  user: User;
  deviceId: string;
}

export interface LoginInput {
  serverUrl: string;
  username: string;
  password: string;
  deviceId?: string;
}
