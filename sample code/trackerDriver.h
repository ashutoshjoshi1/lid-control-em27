#pragma once
#include <Windows.h>
#include <iostream>
#include <string>
#include <vector>


class trackerDriver
{

public:
	static int nMotors;
	static int baud;
	static std::string port;
	static bool connected;
	static HANDLE comPort;
	static DCB comParams;
	static COMMTIMEOUTS comTimeOuts;
	static int selectedComport;
	static char readBuffer[];
	static int32_t registerData[];
	static DWORD readBytes;

//	static std::list<int> comPorts;


	static bool getComPorts();
	static bool openComPort();
	static bool closeComPort();

	static void sendQuery(int slave, int function, char data[], const int dataLength);
	static void diagQuery(int slave);

	static int getResponse();
	static void testsend();
	static void stopMotors();

	static void setMotorSpeed(int slave, int hz);


	static void immediatePositionInc(int slave, int32_t position, int32_t speed, int32_t acceleration);

	static void immediatePositionAbs(int slave, int32_t position, int32_t speed, int32_t acceleration);

	static void writeRegister(int slave, int reg, int value);
	static void writeMultipleRegisters(int slave, int regBase, int nRegisters, char data[]);

	static void readRegisters(int slave, int regBase, int nRegisters);


	static bool isMoving(int slave);
	static bool isInPosition(int slave);

	static int insertIntoBuffer(int32_t in, char buffer[], int position);
	static int32_t readFromBuffer(char buffer[], int position);
private:
	static unsigned int calculateCRC16(char data[], int dataLength);
};