package main

// @title Machine Whisperer API
// @version 1
// @description HTTP API for user accounts, device enrollment, machines, alerts and telemetry.
// @servers.url /
// @externaldocs.url https://github.com/vladgrskkh/machine-whisperer
// @externaldocs.description Machine Whisperer source repository.
// @tag.name internal
// @tag.description Restricted manufacturing operations.
// @tag.name operational
// @tag.description Process health and API documentation.

// @securityDefinitions.bearerauth UserAccessJWT
// @bearerFormat JWT
// @description Send the user access JWT. Refresh and logout use the refresh token in the JSON body.

// @securityDefinitions.bearerauth DeviceBearer
// @description Send the device credential in <device_id>.<secret> form. The device must be claimed. Authentication supplies the telemetry_writer role.
