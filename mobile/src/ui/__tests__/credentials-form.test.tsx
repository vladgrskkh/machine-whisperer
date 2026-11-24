import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';

import { ApiError } from '../../api/errors';
import { CredentialsForm } from '../credentials-form';

it('passes the email and exact password entered by the operator', async () => {
  const onSubmit = jest.fn().mockResolvedValue(undefined);
  render(<CredentialsForm mode="sign-in" onSubmit={onSubmit} />);

  fireEvent.changeText(screen.getByLabelText('Email'), ' operator@example.com ');
  fireEvent.changeText(screen.getByLabelText('Password'), '  exact password  ');
  fireEvent.press(screen.getByRole('button', { name: 'Sign in' }));

  await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({
    email: 'operator@example.com', password: '  exact password  ',
  }));
});

it('shows the minimum password requirement and blocks a short signup password', () => {
  const onSubmit = jest.fn();
  render(<CredentialsForm mode="sign-up" onSubmit={onSubmit} />);

  fireEvent.changeText(screen.getByLabelText('Email'), 'operator@example.com');
  fireEvent.changeText(screen.getByLabelText('Password'), 'short');
  fireEvent.press(screen.getByRole('button', { name: 'Create account' }));

  expect(screen.getByRole('alert')).toHaveTextContent('Use 15–128 characters for your password.');
  expect(onSubmit).not.toHaveBeenCalled();
});

it('shows the public API error when sign-in fails', async () => {
  const onSubmit = jest.fn().mockRejectedValue(new ApiError(401, 'authentication_failed', 'Incorrect email or password.'));
  render(<CredentialsForm mode="sign-in" onSubmit={onSubmit} />);

  fireEvent.changeText(screen.getByLabelText('Email'), 'operator@example.com');
  fireEvent.changeText(screen.getByLabelText('Password'), 'incorrect');
  fireEvent.press(screen.getByRole('button', { name: 'Sign in' }));

  expect(await screen.findByRole('alert')).toHaveTextContent('Incorrect email or password.');
});
